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
    const rec = { id: 'manager-update', label: 'update manager', state: 'running', progress: 0, log: [], sawEvent: true, retiring: false };
    this.jobs = this.jobs.filter(j => j.id !== rec.id); // drop a prior run's record
    this.jobs.push(rec);
    this.showJob(rec);
    this.jobPanelOpen = true;
    // Mutate through the reactive array element (not the raw `rec`) so Alpine repaints the
    // log panel on each line. `this.jobs.find` returns the reactive proxy for this record.
    const live = () => this.jobs.find(j => j.id === rec.id) || rec;
    this.closeManagerUpdateStream();
    const es = new EventSource('/api/v1/manager/update/logs');
    this._managerUpdateES = es;
    es.onmessage = (e) => {
      let ev; try { ev = JSON.parse(e.data); } catch { return; }
      if (ev.kind === 'log' && ev.message) {
        live().log.push(ev.message);
        if (this.job.id === rec.id && this.jobStick) this.$nextTick(() => this.scrollJobToBottom());
      }
    };
    es.onerror = () => {
      // The manager restarting at the end of the update drops the stream — expected. Stop the
      // browser's auto-reconnect and mark the record; the stale banner takes over.
      es.close();
      if (this._managerUpdateES === es) this._managerUpdateES = null;
      const r = live();
      if (r.state === 'running') r.state = 'restarting';
      // 'restarting' produces no further events, so retire the record like any settled job.
      // Left in place it sat in the queue indicator for good and, because it never read as
      // finished, kept every later job from taking the panel.
      this._later(() => { this.jobs = this.jobs.filter(j => j.id !== rec.id); }, 4000);
    };
  },
  closeManagerUpdateStream() {
    if (this._managerUpdateES) { this._managerUpdateES.close(); this._managerUpdateES = null; }
  },
  // Poll the instance marker hard for a few minutes so the restart is noticed promptly. If it
  // never comes the update script failed short of restarting: release the button and say so,
  // rather than leaving it spinning with no way to retry.
  _pollStaleUntilRestart() {
    clearInterval(this._staleTimer);
    let n = 0;
    this._staleTimer = setInterval(() => {
      if (this.stale) { clearInterval(this._staleTimer); return; }
      if (n++ > 120) {
        clearInterval(this._staleTimer);
        this.managerUpdating = false;
        this.managerUpdateError = 'The manager never restarted — check the update output in the job panel.';
        return;
      }
      this.checkInstance();
    }, 2000);
  },
  async createToken() {
    const r = await fetch('/api/v1/auth/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: this.newTokenName, auditAccess: this.newTokenAudit }) });
    if (!r.ok) { alert('Failed to create token: ' + r.status); return; }
    const t = await r.json();
    this.newTokenValue = t.token; this.newTokenName = ''; this.newTokenAudit = false;
    this.refresh();
  },
  async revokeToken(id) {
    const r = await fetch(`/api/v1/auth/tokens/${id}`, { method: 'DELETE' });
    if (!r.ok) alert('Failed to revoke token: ' + r.status);
    this.refresh();
  },
  async downloadBackup() {
    const r = await fetch('/api/v1/backup');
    if (!r.ok) { alert('Backup failed: ' + r.status); return; }
    const data = await r.json();
    const blob = new Blob([JSON.stringify(data)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = 'labassistant-backup.json'; a.click();
    URL.revokeObjectURL(url);
  },
  async restoreBackup(ev) {
    const input = ev.target;
    const file = input.files[0];
    if (!file) return;
    try {
      const text = await file.text();
      const r = await fetch('/api/v1/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: text });
      alert(r.ok ? 'Restored. Restart the manager to apply.' : 'Restore failed.');
    } finally {
      // Clear the picker, or choosing the same file again fires no change event and the
      // restore silently does nothing.
      input.value = '';
    }
  },
};
