// Settings/audit odds and ends: audit-log pagination, the manager self-update flow, API tokens,
// and backup/restore.
export const misc = {
  audit: [],
  auditPage: 1,
  auditPageSize: 25,
  auditError: '',
  managerUpdating: false,
  managerUpdateError: '',
  tokens: [],
  newTokenName: '',
  newTokenAudit: false,
  newTokenValue: '',

  // ---- audit pagination (client-side over the fetched newest-first entries) ----
  auditPages() { return Math.max(1, Math.ceil(this.audit.length / this.auditPageSize)); },
  auditPageSlice() {
    const start = (this.auditPage - 1) * this.auditPageSize;
    return this.audit.slice(start, start + this.auditPageSize);
  },
  async updateManager() {
    if (this.managerUpdating) return;
    this.managerUpdateError = '';
    this.managerUpdating = true;
    try {
      const r = await fetch('/api/v1/manager/update', { method: 'POST' });
      if (!r.ok) {
        const e = await r.json().catch(() => ({}));
        this.managerUpdateError = (e.error && e.error.message) || 'Failed to start update.';
        this.managerUpdating = false;
        return;
      }
      // Surface the update script's output in the jobs panel by tailing its log.
      this.watchManagerUpdate();
      // The restart will change the instance marker; the poller then flips the stale banner.
      // Speed that up by polling more aggressively for a bit.
      this._pollStaleUntilRestart();
    } catch (e) {
      this.managerUpdateError = 'Failed to reach the manager.';
      this.managerUpdating = false;
    }
  },
  // watchManagerUpdate streams the manager self-update log into a jobs-panel record. Unlike a
  // normal job it has no terminal state event: the manager restarts at the end, which drops
  // the stream (handled in onerror). The stale banner then prompts re-login.
  watchManagerUpdate() {
    const rec = { id: 'manager-update', label: 'update manager', state: 'running', progress: 0, log: [] };
    this.jobs = this.jobs.filter(j => j.id !== rec.id); // drop a prior run's record
    this.jobs.push(rec);
    this.showJob(rec);
    this.jobPanelOpen = true;
    // Mutate through the reactive array element (not the raw `rec`) so Alpine repaints the
    // log panel on each line. `this.jobs.find` returns the reactive proxy for this record.
    const live = () => this.jobs.find(j => j.id === rec.id) || rec;
    const es = new EventSource('/api/v1/manager/update/logs');
    es.onmessage = (e) => {
      let ev; try { ev = JSON.parse(e.data); } catch { return; }
      if (ev.kind === 'log' && ev.message) {
        live().log.push(ev.message);
        if (this.job.id === rec.id && this.jobStick) this.$nextTick(() => { const el = this.$refs.jobLog; if (el) el.scrollTop = el.scrollHeight; });
      }
    };
    es.onerror = () => {
      // The manager restarting at the end of the update drops the stream — expected. Stop the
      // browser's auto-reconnect and mark the record; the stale banner takes over.
      es.close();
      const r = live();
      if (r.state === 'running') r.state = 'restarting';
    };
  },
  _pollStaleUntilRestart() {
    let n = 0;
    const t = setInterval(() => {
      if (this.stale || n++ > 120) { clearInterval(t); return; }
      this.checkInstance();
    }, 2000);
  },
  async createToken() {
    const r = await fetch('/api/v1/auth/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: this.newTokenName, auditAccess: this.newTokenAudit }) });
    if (r.ok) { const t = await r.json(); this.newTokenValue = t.token; this.newTokenName = ''; this.newTokenAudit = false; this.refresh(); }
  },
  async revokeToken(id) { await fetch(`/api/v1/auth/tokens/${id}`, { method: 'DELETE' }); this.refresh(); },
  async downloadBackup() {
    const data = await (await fetch('/api/v1/backup')).json();
    const blob = new Blob([JSON.stringify(data)], { type: 'application/json' });
    const a = document.createElement('a'); a.href = URL.createObjectURL(blob); a.download = 'labassistant-backup.json'; a.click();
  },
  async restoreBackup(ev) {
    const file = ev.target.files[0]; if (!file) return;
    const text = await file.text();
    const r = await fetch('/api/v1/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: text });
    alert(r.ok ? 'Restored. Restart the manager to apply.' : 'Restore failed.');
  },
};
