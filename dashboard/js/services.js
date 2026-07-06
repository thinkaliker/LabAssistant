// Services page: host-tagged sorting, service status tags, and the compose file editor.

// CodeMirror instance for the compose editor, kept OUT of Alpine's reactive state so its
// internal objects aren't wrapped in reactive proxies (which breaks the editor). Module-scoped
// because every user of it lives in this file, and there is one app() component per page.
let composeCM = null;

export const services = {
  services: { stacks: [] },
  compose: { open: false, hostId: '', stack: '', path: '', multiFile: false, loading: false, busy: false, error: '', status: '' },

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
  // ---- compose editor ----
  // editCompose reads the file first and only opens the side panel once that succeeds. If the
  // read needs a sudo password, the sudo banner appears and submitSudo() routes the retry's
  // result back through openComposeFromJob().
  async editCompose(st) {
    try {
      const out = await this.dispatchSilent(st.hostId, 'duo', 'read-compose', { stack: st.name });
      if (!out || !out.jobId) { alert('Could not start compose read.'); return; }
      const job = await this.awaitJob(out.jobId);
      if (job && job.state === 'needs_sudo_password') { this.refresh(); return; }
      this.openComposeFromJob(job);
    } catch (e) { console.error(e); alert('Error loading compose file.'); }
  },
  openComposeFromJob(job) {
    if (!job || job.state !== 'succeeded' || !job.result) {
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
    const job = await this.awaitJob(out.jobId);
    if (job && job.state === 'needs_sudo_password') { this.compose.error = 'Sudo password required — provide it in the banner above, then save again.'; this.refresh(); return false; }
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
