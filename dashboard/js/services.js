// Services page: host-tagged sorting, service status tags, and the compose file editor.

// CodeMirror instance for the compose editor, kept OUT of Alpine's reactive state so its
// internal objects aren't wrapped in reactive proxies (which breaks the editor). Module-scoped
// because every user of it lives in this file, and there is one app() component per page.
let composeCM = null;

export const services = {
  services: { stacks: [] },
  compose: { open: false, hostId: '', stack: '', path: '', multiFile: false, loading: false, busy: false, error: '', status: '' },
  checkingServices: [], // "hostId/stack/service" keys with an in-flight single-image check
  updatingServices: [], // same keys, for an in-flight single-service pull/recreate

  // sortByHost orders any host-tagged list (items carry hostId/hostName) by the same
  // hostSort key used for the Hosts page, so Services and Updates stay in sync. label(item)
  // supplies a secondary key (stack/service) so rows under one host keep a stable order.
  sortByHost(list, label) {
    const hostOf = (id) => this.hosts.find(x => x.id === id) || {};
    return [...(list || [])].sort((a, b) => {
      const ha = hostOf(a.hostId), hb = hostOf(b.hostId);
      let c;
      if (this.hostSort === 'ip') c = this.ipKey(ha.ip).localeCompare(this.ipKey(hb.ip));
      else c = (a.hostName || ha.name || '').localeCompare(b.hostName || hb.name || '', undefined, { sensitivity: 'base' });
      if (c !== 0) return c;
      return label ? label(a).localeCompare(label(b), undefined, { sensitivity: 'base' }) : 0;
    });
  },
  // A service tag prefers its docker healthcheck state (healthy/unhealthy/starting) over the
  // raw running/stopped status, so an unhealthy-but-running container reads at a glance.
  svcLabel(sv) { return sv.health || sv.status; },
  svcClass(sv) {
    if (sv.health) return { healthy: 'is-success', unhealthy: 'is-danger', starting: 'is-warning' }[sv.health] || 'is-info';
    return this.statusClass(sv.status);
  },
  // ---- per-service update check ----
  // The Updates page only checks a whole host (qup + every duo image), which is minutes of
  // registry reads when the question is "is this one container stale?". checkService scopes
  // duo check-updates to one stack/service so a single image can be re-checked on its own.
  //
  // Keyed by host+stack+service rather than a flag on the service object: the projections are
  // replaced wholesale on every refresh, so any state stored on a row is lost mid-check.
  svcKey(st, sv) { return st.hostId + '/' + st.name + '/' + sv.name; },
  svcChecking(st, sv) { return this.checkingServices.includes(this.svcKey(st, sv)); },
  async checkService(st, sv) {
    const key = this.svcKey(st, sv);
    if (this.svcChecking(st, sv)) return;
    this.checkingServices.push(key);
    try {
      const out = await this.dispatchSilent(st.hostId, 'duo', 'check-updates', { stack: st.name, service: sv.name });
      // Watched, not silent: this is a check the user explicitly asked for on one image, so the
      // log ("up to date" / "update available") is the answer they are waiting on. Awaiting the
      // job before refreshing is what makes a newly-found update actually render — a
      // fire-and-forget dispatch left the row unchanged and the button looking dead.
      if (out && out.jobId) {
        await this.watchJob(out.jobId, `duo check-updates ${st.name}/${sv.name}`,
          { hostId: st.hostId, module: 'duo', action: 'check-updates' });
      }
      await this.refresh();
    } finally {
      this.checkingServices = this.checkingServices.filter(x => x !== key);
    }
  },
  // updateSvc pulls and recreates one service. It routes through runHostUpdates so a single
  // service gets the same treatment as the Updates page: the host is marked busy for the
  // duration (duo serializes per host anyway), the job streams into the docked panel, and a
  // policy that gates the destructive update behind an approval surfaces the banner.
  //
  // The row spinner is tracked separately from that host-wide busy flag so only the service
  // actually being updated spins, while every other row on the host reads as disabled — a
  // click there would be swallowed by runHostUpdates' guard, so it must not look clickable.
  svcUpdating(st, sv) { return this.updatingServices.includes(this.svcKey(st, sv)); },
  async updateSvc(st, sv) {
    const key = this.svcKey(st, sv);
    if (this.svcUpdating(st, sv) || this.hostUpdating(st.hostId)) return;
    this.updatingServices.push(key);
    try {
      await this.updateService({ hostId: st.hostId, stack: st.name, service: sv.name });
    } finally {
      this.updatingServices = this.updatingServices.filter(x => x !== key);
    }
  },
  // ---- compose editor ----
  // editCompose reads the file first and only opens the side panel once that succeeds. If the
  // read needs a sudo password, the sudo banner appears and submitSudo() routes the retry's
  // result back through openComposeFromJob().
  async editCompose(st) {
    try {
      const out = await this.dispatchSilent(st.hostId, 'duo', 'read-compose', { stack: st.name });
      if (!out || !out.jobId) { alert('Could not start compose read.'); return; }
      const res = await this.awaitJob(out.jobId);
      if (res.job && res.job.state === 'needs_sudo_password') { this.refresh(); return; }
      this.openComposeFromJob(res);
    } catch (e) { console.error(e); alert('Error loading compose file.'); }
  },
  // Takes an awaitJob result, so a read that is merely slow can be reported as such instead of
  // as an "unknown error".
  openComposeFromJob(outcome) {
    const job = outcome && outcome.job;
    if (!job || job.state !== 'succeeded' || !job.result) {
      if (outcome && outcome.timedOut) { alert('Still reading the compose file — check the job panel, then try again.'); return; }
      alert('Failed to read compose file: ' + ((job && job.error) || 'unknown error'));
      return;
    }
    const res = typeof job.result === 'string' ? JSON.parse(job.result) : job.result;
    let stack = res.stack || '';
    try { const p = typeof job.params === 'string' ? JSON.parse(job.params) : job.params; if (p && p.stack) stack = p.stack; } catch (e) { /* keep res.stack */ }
    if (composeCM) { composeCM.toTextArea(); composeCM = null; }
    this.compose = { open: true, hostId: job.hostId, stack, path: res.path || '', multiFile: !!res.multiFile, loading: false, busy: false, error: '', status: '' };
    if (this.compose.multiFile) return;
    this.$nextTick(() => this.mountEditor(res.content || ''));
  },
  mountEditor(content) {
    const ta = this.$refs.composeEditor;
    if (!ta) return;
    ta.value = content;
    if (!window.CodeMirror) { // fallback: plain textarea with basic tab handling
      ta.style.cssText = 'width:100%;height:60vh;font-family:monospace';
      ta.onkeydown = (e) => {
        if (e.key === 'Tab') { e.preventDefault(); const s = ta.selectionStart, en = ta.selectionEnd; ta.value = ta.value.slice(0, s) + '  ' + ta.value.slice(en); ta.selectionStart = ta.selectionEnd = s + 2; }
      };
      return;
    }
    composeCM = CodeMirror.fromTextArea(ta, {
      mode: 'yaml',
      lineNumbers: true,
      indentUnit: 2,
      tabSize: 2,
      indentWithTabs: false,
      gutters: ['CodeMirror-lint-markers'],
      lint: true,
      extraKeys: {
        Tab: (cm) => { if (cm.somethingSelected()) cm.indentSelection('add'); else cm.replaceSelection('  ', 'end'); },
        'Shift-Tab': (cm) => cm.indentSelection('subtract'),
      },
    });
    composeCM.setSize(null, '60vh');
    setTimeout(() => composeCM && composeCM.refresh(), 50);
  },
  editorValue() {
    if (composeCM) return composeCM.getValue();
    const ta = this.$refs.composeEditor;
    return ta ? ta.value : '';
  },
  async writeCompose(content) {
    const out = await this.dispatchSilent(this.compose.hostId, 'duo', 'write-compose', { stack: this.compose.stack, content });
    if (!out || !out.jobId) { this.compose.error = 'Could not start save.'; return false; }
    const res = await this.awaitJob(out.jobId);
    const job = res.job;
    if (job && job.state === 'needs_sudo_password') { this.compose.error = 'Sudo password required — provide it in the banner above, then save again.'; this.refresh(); return false; }
    // A slow write is not a failed one. Calling it "Save failed" invited the user to save
    // again, writing the file twice.
    if (res.timedOut) { this.compose.error = 'Save is still running — watch the job panel, and re-read the file before saving again.'; return false; }
    if (!job || job.state !== 'succeeded') { this.compose.error = (job && job.error) || 'Save failed (see validation message).'; return false; }
    return true;
  },
  async saveCompose() {
    this.compose.error = ''; this.compose.status = ''; this.compose.busy = true;
    const ok = await this.writeCompose(this.editorValue());
    this.compose.busy = false;
    if (ok) { this.compose.status = 'Saved.'; this.refresh(); }
  },
  async saveAndRedeploy() {
    this.compose.error = ''; this.compose.status = ''; this.compose.busy = true;
    const ok = await this.writeCompose(this.editorValue());
    this.compose.busy = false;
    if (!ok) return;
    this.compose.status = 'Saved. Redeploy queued — confirm it in the approvals banner.';
    await this.runAction(this.compose.hostId, 'duo', 'deploy', { stack: this.compose.stack });
  },
  closeCompose() {
    if (composeCM) { composeCM.toTextArea(); composeCM = null; }
    this.compose = { open: false, hostId: '', stack: '', path: '', multiFile: false, loading: false, busy: false, error: '', status: '' };
  },
};
