// Alpine component backing the LabAssistant dashboard (index.html). Loaded as a plain
// (non-deferred) script before the deferred Alpine bundle, so app() is defined by the time
// Alpine initializes x-data="app()".
function app() {
  // CodeMirror instance for the compose editor, kept OUT of Alpine's reactive state so its
  // internal objects aren't wrapped in reactive proxies (which breaks the editor).
  let composeCM = null;
  return {
    page: 'overview',
    navOpen: false, // mobile navbar-burger toggle (collapsed by default on narrow viewports)
    overview: {},
    hosts: [],
    services: { stacks: [] },
    updates: { os: [], containers: [] },
    compose: { open: false, hostId: '', stack: '', path: '', multiFile: false, loading: false, busy: false, error: '', status: '' },
    tasks: [],
    approvals: [],
    sudoPrompts: [],
    sudoModal: { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' },
    audit: [],
    auditPage: 1,
    auditPageSize: 25,
    expanded: null,
    hostSort: 'name', // 'name' | 'ip' — how the Hosts list is ordered
    addHostOpen: false,
    taskOpen: false,
    cronFields: false, // Add Task: false = raw cron string, true = 5 separate fields
    cronParts: { m: '*', h: '*', dom: '*', mon: '*', dow: '*' },
    newHost: { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false, connMode: 'manager_dial', connPort: null },
    newTask: { name: '', schedule: '', module: '', action: '', hostIds: [], misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false },
    job: { id: '', label: '', state: '', progress: 0, log: [] }, // the job currently on screen
    jobs: [], // all active jobs (queued/running + briefly-settled), shown in the queue indicator
    jobPanelOpen: false, // whether the docked log panel is visible
    jobStick: true, // keep the job log pinned to the newest line until the user scrolls up
    jobPanelHeight: 0, // px override for the docked job panel (0 = CSS default of 33vh)
    logView: { open: false, title: '', lines: [], es: null },
    ready: false,
    needsLogin: false,
    authUser: '',
    login: { username: '', password: '' },
    loginError: '',
    settings: { logLevel: 'info', defaultTimezone: '' },
    managerUpdating: false,
    managerUpdateError: '',
    instanceId: '', // manager process marker; a change means it restarted underneath us
    stale: false, // manager restarted, so this page's session/state is no longer valid
    tokens: [],
    newTokenName: '',
    newTokenAudit: false,
    newTokenValue: '',
    auditError: '',
    cfg: { open: false, hostId: '', module: '', fields: [], values: {} },
    act: { open: false, hostId: '', module: '', action: '', destructive: false, fields: [], values: {}, error: '' },
    uninstall: { open: false, hostId: '', hostName: '', online: false, sshUser: '', sshPassword: '' },
    revive: { open: false, hostId: '', hostName: '', sshUser: '', sshPassword: '' },

    async init() {
      try {
        const r = await fetch('/api/v1/auth/session');
        if (!r.ok) { this.needsLogin = true; return; }
        const s = await r.json();
        this.instanceId = s.instance || '';
        this.authUser = s.authRequired ? (s.username || '') : '';
        if (s.authRequired && !s.authenticated) { this.needsLogin = true; return; }
      } catch (e) {
        console.error(e);
        this.needsLogin = true;
        return;
      } finally {
        this.ready = true;
      }
      this.start();
    },
    start() {
      this.refresh();
      const es = new EventSource('/api/v1/events');
      es.onmessage = () => this.refresh();
      // Poll the manager's instance marker (public endpoint, works in auth and open mode) so a
      // restart underneath us surfaces the stale banner even when the SSE stream can't reconnect.
      clearInterval(this._instanceTimer);
      this._instanceTimer = setInterval(() => this.checkInstance(), 10000);
    },
    async checkInstance() {
      if (this.stale) return;
      try {
        const s = await (await fetch('/api/v1/auth/session')).json();
        if (this.instanceId && s.instance && s.instance !== this.instanceId) this.stale = true;
        else if (s.authRequired && !s.authenticated) this.stale = true;
      } catch (e) { /* manager down mid-restart; next tick retries */ }
    },
    reloadForLogin() { window.location.reload(); },
    async doLogin() {
      const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.login) });
      if (!r.ok) { this.loginError = 'Invalid credentials'; return; }
      const data = await r.json().catch(() => ({}));
      this.authUser = data.username || this.login.username;
      this.loginError = ''; this.login.password = ''; this.needsLogin = false; this.start();
    },
    async logout() {
      await fetch('/api/v1/auth/logout', { method: 'POST' });
      this.authUser = ''; this.needsLogin = true;
    },
    async refresh() {
      try {
        this.overview = await (await fetch('/api/v1/overview')).json();
        this.hosts = await (await fetch('/api/v1/hosts')).json();
        this.services = await (await fetch('/api/v1/services')).json();
        this.updates = await (await fetch('/api/v1/updates')).json();
        this.tasks = await (await fetch('/api/v1/tasks')).json();
        this.approvals = await (await fetch('/api/v1/approvals')).json();
        this.sudoPrompts = await (await fetch('/api/v1/sudo')).json();
        const ar = await fetch('/api/v1/audit');
        if (ar.ok) { this.audit = await ar.json(); this.auditError = ''; if (this.auditPage > this.auditPages()) this.auditPage = this.auditPages(); }
        else { this.audit = []; this.auditError = ar.status === 403 ? 'Audit access not permitted for this credential.' : 'Failed to load audit log.'; }
        this.settings = await (await fetch('/api/v1/settings')).json();
        this.tokens = await (await fetch('/api/v1/auth/tokens')).json();
      } catch (e) { console.error(e); }
    },
    // ---- audit pagination (client-side over the fetched newest-first entries) ----
    auditPages() { return Math.max(1, Math.ceil(this.audit.length / this.auditPageSize)); },
    auditPageSlice() {
      const start = (this.auditPage - 1) * this.auditPageSize;
      return this.audit.slice(start, start + this.auditPageSize);
    },
    async saveSettings() {
      await fetch('/api/v1/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.settings) });
      this.refresh();
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
    async openConfig(hostId, m) {
      this.cfg = { open: true, hostId, module: m.name, fields: [], values: {} };
      const r = await (await fetch(`/api/v1/hosts/${hostId}/modules/${m.name}/config`)).json();
      const props = (r.schema && JSON.parse(typeof r.schema === 'string' ? r.schema : JSON.stringify(r.schema)).properties) || {};
      this.cfg.fields = Object.entries(props).map(([key, v]) => ({ key, title: v.title, secret: !!v.secret }));
      this.cfg.values = r.config || {};
    },
    async saveModuleConfig() {
      await fetch(`/api/v1/hosts/${this.cfg.hostId}/modules/${this.cfg.module}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.cfg.values) });
      this.cfg.open = false;
    },
    hostName(id) { const h = this.hosts.find(x => x.id === id); return h ? h.name : id; },
    // ipKey turns an IP into a zero-padded string so a plain string compare orders octets
    // numerically (so .10 sorts after .9).
    ipKey(ip) { return (ip || '').split('.').map(o => String(parseInt(o, 10) || 0).padStart(3, '0')).join('.'); },
    // sortedHosts returns a stable copy of hosts ordered by the chosen key so the list
    // doesn't reshuffle as the backend returns hosts in map/enroll order.
    sortedHosts() {
      return [...this.hosts].sort((a, b) => {
        if (this.hostSort === 'ip') {
          const c = this.ipKey(a.ip).localeCompare(this.ipKey(b.ip));
          if (c !== 0) return c;
        }
        return (a.name || '').localeCompare(b.name || '', undefined, { sensitivity: 'base' });
      });
    },
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
    openTask() {
      this.newTask = { name: '', schedule: '', module: '', action: '', hostIds: [], misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false };
      this.cronFields = false;
      this.cronParts = { m: '*', h: '*', dom: '*', mon: '*', dow: '*' };
      this.taskOpen = true;
    },
    // Toggle between the raw cron string and the 5-field editor, keeping both in sync.
    toggleCronFields() {
      if (!this.cronFields) {
        // switching to fields: parse the current string, filling gaps with '*'
        const p = (this.newTask.schedule || '').trim().split(/\s+/);
        this.cronParts = { m: p[0] || '*', h: p[1] || '*', dom: p[2] || '*', mon: p[3] || '*', dow: p[4] || '*' };
      }
      this.cronFields = !this.cronFields;
    },
    // Recompose the cron string from the 5 fields (called on each field edit).
    syncCron() {
      const c = this.cronParts;
      this.newTask.schedule = [c.m, c.h, c.dom, c.mon, c.dow].map(x => (x || '').trim() || '*').join(' ');
    },
    // Unique module names across all reporting hosts, alphabetical, for the Add Task dropdown.
    taskModuleNames() {
      const set = new Set();
      for (const h of this.hosts) for (const m of (h.modules || [])) set.add(m.name);
      return [...set].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
    },
    // Actions available for the currently-selected module, unioned across hosts, alphabetical.
    taskModuleActions() {
      const set = new Set();
      for (const h of this.hosts) for (const m of (h.modules || [])) {
        if (m.name === this.newTask.module) for (const a of (m.actions || [])) set.add(a.name);
      }
      return [...set].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
    },
    // Hosts sorted by name so the Add Task checkbox list doesn't reshuffle on refresh.
    taskHostsSorted() {
      return [...this.hosts].sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, { sensitivity: 'base' }));
    },
    toggleTaskHost(id) {
      const i = this.newTask.hostIds.indexOf(id);
      if (i >= 0) this.newTask.hostIds.splice(i, 1); else this.newTask.hostIds.push(id);
    },
    async submitTask() {
      const r = await fetch('/api/v1/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.newTask) });
      if (!r.ok) { const e = await r.json().catch(() => ({})); alert('create failed: ' + (e.error?.message || r.status)); return; }
      this.taskOpen = false;
      this.refresh();
    },
    async removeTask(id) {
      await fetch(`/api/v1/tasks/${id}`, { method: 'DELETE' });
      this.refresh();
    },
    async confirmApproval(id) {
      const r = await fetch(`/api/v1/approvals/${id}/confirm`, { method: 'POST' });
      if (r.ok) { const { jobId } = await r.json(); this.refresh(); if (jobId) this.watchJob(jobId, 'approved action'); }
    },
    async rejectApproval(id) {
      await fetch(`/api/v1/approvals/${id}/reject`, { method: 'POST' });
      this.refresh();
    },
    openSudo(p) {
      this.sudoModal = { open: true, id: p.id, hostId: p.hostId, module: p.module, action: p.action, password: '', error: '' };
      this.$nextTick(() => this.$refs.sudoInput && this.$refs.sudoInput.focus());
    },
    async submitSudo() {
      const r = await fetch(`/api/v1/sudo/${this.sudoModal.id}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: this.sudoModal.password }),
      });
      if (!r.ok) {
        this.sudoModal.error = r.status === 404 ? 'This prompt is no longer pending.' : 'Failed to submit password.';
        return;
      }
      const action = this.sudoModal.action;
      const mod = this.sudoModal.module;
      const { jobId } = await r.json().catch(() => ({}));
      this.sudoModal = { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' };
      this.refresh();
      if (!jobId) return;
      // A re-dispatched compose read feeds the editor instead of the generic job modal.
      if (action === 'read-compose') { this.openComposeFromJob(await this.awaitJob(jobId)); }
      else this.watchJob(jobId, mod + '/' + action);
    },
    async cancelSudo(id) {
      await fetch(`/api/v1/sudo/${id}/cancel`, { method: 'POST' });
      if (this.sudoModal.id === id) this.sudoModal.open = false;
      this.refresh();
    },
    toggle(id) { this.expanded = this.expanded === id ? null : id; },
    openUninstall(h) {
      this.uninstall = { open: true, hostId: h.id, hostName: h.name, online: h.status === 'online', sshUser: h.sshUser || '', sshPassword: '' };
    },
    async submitUninstall() {
      const body = { sshUser: this.uninstall.sshUser, sshPassword: this.uninstall.sshPassword };
      const r = await fetch(`/api/v1/hosts/${this.uninstall.hostId}/uninstall`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      this.uninstall.open = false;
      if (!r.ok) { alert('uninstall failed'); return; }
      const { jobId } = await r.json();
      const hostName = this.uninstall.hostName;
      await this.refresh();
      if (jobId) this.watchJob(jobId, 'uninstall ' + hostName);
    },
    openRevive(h) {
      this.revive = { open: true, hostId: h.id, hostName: h.name, sshUser: h.sshUser || '', sshPassword: '' };
    },
    async submitRevive() {
      const body = { sshUser: this.revive.sshUser, sshPassword: this.revive.sshPassword };
      const r = await fetch(`/api/v1/hosts/${this.revive.hostId}/revive`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      this.revive.open = false;
      if (!r.ok) { alert('revive failed'); return; }
      const { jobId } = await r.json();
      const hostName = this.revive.hostName;
      await this.refresh();
      if (jobId) this.watchJob(jobId, 'revive ' + hostName);
    },
    // Overall system health, used to tint the flask icon like a status light.
    //   crit (red)  — something needs attention now: a host down, an unhealthy container,
    //                 or a sudo password blocking a job.
    //   warn (amber)— degraded but not urgent: updates available, pending approvals,
    //                 a host enrolling, or a stopped/partial service.
    //   good (green)— all hosts online, all services healthy, nothing pending.
    overallStatus() {
      if (this.sudoPrompts.length) return 'crit';
      if (this.hosts.some(h => h.status === 'offline' || h.status === 'error')) return 'crit';
      for (const st of (this.services.stacks || [])) {
        for (const sv of (st.services || [])) {
          if (sv.health === 'unhealthy') return 'crit';
        }
      }
      if (this.approvals.length) return 'warn';
      if (this.hosts.some(h => h.status !== 'online')) return 'warn'; // enrolling / unknown
      if ((this.overview.updates?.packages ?? 0) > 0) return 'warn';
      if ((this.updates?.containers?.length ?? 0) > 0) return 'warn';
      for (const st of (this.services.stacks || [])) {
        if (st.status === 'partial' || st.status === 'stopped') return 'warn';
        for (const sv of (st.services || [])) {
          if (sv.status === 'stopped' || sv.status === 'exited' || sv.updateAvailable) return 'warn';
        }
      }
      return 'good';
    },
    // Fill color for the flask liquid — the visible status light.
    statusColor() {
      return { good: '#48c78e', warn: '#ffb454', crit: '#f14668' }[this.overallStatus()];
    },
    statusTitle() {
      return { good: 'All systems healthy', warn: 'Attention: updates or issues pending',
               crit: 'Action required: a host or service needs attention' }[this.overallStatus()];
    },
    statusClass(s) {
      return {
        online: 'is-success', offline: 'is-danger', enrolling: 'is-warning',
        error: 'is-danger', succeeded: 'is-success', failed: 'is-danger',
        running: 'is-success', partial: 'is-warning', pending: 'is-warning',
        stopped: 'is-danger'
      }[s] || 'is-light';
    },
    // A service tag prefers its docker healthcheck state (healthy/unhealthy/starting) over the
    // raw running/stopped status, so an unhealthy-but-running container reads at a glance.
    svcLabel(sv) { return sv.health || sv.status; },
    svcClass(sv) {
      if (sv.health) return { healthy: 'is-success', unhealthy: 'is-danger', starting: 'is-warning' }[sv.health] || 'is-info';
      return this.statusClass(sv.status);
    },
    // hostUpdates totals a host's pending updates: qup package counts plus duo services with a
    // newer image. Mirrors the manager's overview tally, computed client-side from host modules.
    hostUpdates(h) {
      let n = 0;
      for (const m of (h.modules || [])) {
        let s = m.status;
        if (!s) continue;
        if (typeof s === 'string') { try { s = JSON.parse(s); } catch (e) { continue; } }
        if (typeof s.count === 'number') n += s.count;
        if (Array.isArray(s.stacks)) {
          for (const st of s.stacks) for (const sv of (st.services || [])) if (sv.updateAvailable) n++;
        }
      }
      return n;
    },
    capabilities(m) {
      const c = m.detection && m.detection.capabilities || {};
      return Object.entries(c).map(([k, v]) => `${k}=${v}`).join(' ');
    },
    // humanBytes renders a byte count in binary units (KiB/MiB/GiB/...).
    humanBytes(n) {
      n = Number(n) || 0;
      if (n < 1024) return `${n} B`;
      const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
      let i = -1;
      do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
      return `${n.toFixed(1)} ${units[i]}`;
    },
    // pct returns used/total as a whole-number percentage (0 when total is unknown).
    pct(used, total) {
      total = Number(total) || 0;
      if (total <= 0) return 0;
      return Math.round((Number(used) || 0) / total * 100);
    },
    // humanUptime renders seconds as a compact "Nd Nh Nm" duration.
    humanUptime(s) {
      s = Number(s) || 0;
      const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
      if (d > 0) return `${d}d ${h}h`;
      if (h > 0) return `${h}h ${m}m`;
      return `${m}m`;
    },
    // isTerminalJob reports whether a job state is final (no more events will come).
    isTerminalJob(s) { return s === 'succeeded' || s === 'failed' || s === 'timed_out'; },
    // showJob puts a job's record on screen without forcing the panel open — opening is lazy
    // (see the event handler) so a sudo hand-off or silent success doesn't flash the panel.
    showJob(rec) {
      this.job = rec;
      this.jobStick = true;
      this.$nextTick(() => { const el = this.$refs.jobLog; if (el) el.scrollTop = el.scrollHeight; });
    },
    // selectJob is the user clicking a queue chip to bring that job's log to the front.
    selectJob(rec) { this.showJob(rec); this.jobPanelOpen = true; },
    watchJob(jobId, label) {
      // Each job gets its OWN record so overlapping jobs (e.g. several started in a row, or
      // parallel jobs across hosts) don't cross-contaminate one shared log. All non-terminal
      // jobs show up in the queue indicator; the panel displays one at a time.
      const rec = { id: jobId, label: label || ('job ' + String(jobId).slice(0, 6)), state: 'queued', progress: 0, log: [] };
      this.jobs.push(rec);
      // Adopt the new job on screen when nothing live is showing (or the panel is closed);
      // otherwise leave the current job up and let this one wait in the queue indicator.
      if (!this.jobPanelOpen || !this.job.id || this.isTerminalJob(this.job.state)) this.showJob(rec);
      const es = new EventSource(`/api/v1/jobs/${jobId}/events`);
      es.onmessage = (e) => {
        const ev = JSON.parse(e.data).payload;
        if (ev.kind === 'log' && ev.message) {
          rec.log.push(ev.message);
          if (rec.state === 'queued') rec.state = 'running';
          this.adoptIfIdle(rec);
          // Only steal focus/scroll for the job actually on screen. jobStick is driven by the
          // user's own scrolling (see onJobScroll), so a fast burst can't stop the autoscroll.
          if (this.job.id === rec.id) {
            this.jobPanelOpen = true;
            if (this.jobStick) this.$nextTick(() => { const el = this.$refs.jobLog; if (el) el.scrollTop = el.scrollHeight; });
          }
        }
        if (ev.kind === 'progress') {
          rec.progress = ev.progress;
          if (rec.state === 'queued') rec.state = 'running';
          this.adoptIfIdle(rec);
          if (this.job.id === rec.id) this.jobPanelOpen = true;
        }
        if (ev.kind === 'state') {
          rec.state = ev.state;
          if (ev.state === 'needs_sudo_password' || this.isTerminalJob(ev.state)) {
            es.close();
            this.refresh();
            this.finishJob(rec, ev.state);
          }
        }
      };
    },
    // adoptIfIdle brings a job to the front when nothing live is showing (first run, or the
    // previously shown job has finished), so a job that was queued behind another surfaces on
    // its own once it starts producing output.
    adoptIfIdle(rec) {
      if (this.job.id !== rec.id && (!this.job.id || this.isTerminalJob(this.job.state))) this.showJob(rec);
    },
    // finishJob settles a terminal job: decide whether the panel stays up, then retire it from
    // the queue indicator. A still-queued job takes over the panel later via adoptIfIdle.
    finishJob(rec, state) {
      if (this.job.id === rec.id) {
        // A sudo prompt or a clean output-less success hands off elsewhere — keep the panel
        // closed. A failure, or a success with output, is worth showing.
        this.jobPanelOpen = !(state === 'needs_sudo_password' || (state === 'succeeded' && rec.log.length === 0));
      }
      // Drop it from the indicator: sudo hand-offs immediately, others after a beat so the user
      // sees them settle. If it's still the shown job, the log stays up until the panel closes.
      const retire = () => { this.jobs = this.jobs.filter(j => j.id !== rec.id); };
      if (state === 'needs_sudo_password') retire();
      else setTimeout(retire, 4000);
    },
    // onJobScroll re-arms or releases autoscroll from the user's scroll position: at (or near)
    // the bottom re-pins; scrolling up to read history releases the pin. Programmatic scrolls
    // land at the bottom, so they simply keep jobStick true.
    onJobScroll(e) {
      const el = e.target;
      this.jobStick = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    },
    // startJobResize drags the panel's top edge to grow/shrink the docked job output. Pointer
    // events cover mouse + touch; height is clamped between a sensible floor and ~92vh.
    startJobResize(e) {
      e.preventDefault();
      const startY = e.clientY;
      const panel = this.$refs.jobPanel;
      const startH = panel ? panel.getBoundingClientRect().height : 0;
      const min = 176, max = window.innerHeight * 0.92;
      const onMove = (ev) => {
        this.jobPanelHeight = Math.max(min, Math.min(max, startH + (startY - ev.clientY)));
      };
      const onUp = () => {
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup', onUp);
        document.body.style.userSelect = '';
      };
      document.body.style.userSelect = 'none';
      window.addEventListener('pointermove', onMove);
      window.addEventListener('pointerup', onUp);
    },
    // startAction inspects the action's params schema: param-less actions dispatch
    // immediately, otherwise a schema-driven form collects the params first.
    startAction(hostId, module, a) {
      let schema = a.paramsSchema || null;
      if (typeof schema === 'string') { try { schema = JSON.parse(schema); } catch (e) { schema = null; } }
      const props = schema && schema.properties ? schema.properties : null;
      if (!props || Object.keys(props).length === 0) { this.runAction(hostId, module, a.name); return; }
      const required = schema.required || [];
      this.act = {
        open: true, hostId, module, action: a.name, destructive: !!a.destructive, error: '',
        fields: Object.entries(props).map(([key, v]) => ({ key, title: v.title || key, required: required.includes(key) })),
        values: {},
      };
    },
    submitAction() {
      const params = {};
      for (const f of this.act.fields) {
        const val = this.act.values[f.key];
        if (val !== undefined && val !== '') params[f.key] = val;
      }
      const missing = this.act.fields.filter(f => f.required && !(f.key in params));
      if (missing.length) { this.act.error = 'Required: ' + missing.map(f => f.title).join(', '); return; }
      const { hostId, module, action } = this.act;
      this.act.open = false;
      this.runAction(hostId, module, action, Object.keys(params).length ? params : null);
    },
    async runAction(hostId, mod, action, params) {
      const opts = { method: 'POST' };
      if (params) { opts.headers = { 'Content-Type': 'application/json' }; opts.body = JSON.stringify(params); }
      const r = await fetch(`/api/v1/hosts/${hostId}/modules/${mod}/actions/${action}`, opts);
      if (!r.ok) {
        const rec = { id: 'err-' + Date.now(), label: mod + '/' + action, state: 'failed', progress: 0, log: ['dispatch failed'] };
        this.showJob(rec); this.jobPanelOpen = true;
        return;
      }
      const out = await r.json();
      if (out.approvalId) {
        // No modal — the queued action surfaces in the "Pending approvals" banner. Refresh so
        // it appears, then scroll it into view so the user sees it without a popup.
        await this.refresh();
        window.scrollTo({ top: 0, behavior: 'smooth' });
        return;
      }
      if (out.jobId) this.watchJob(out.jobId, mod + '/' + action);
    },
    svcAction(stack, service, action) {
      const params = service ? { stack: stack.name, service } : { stack: stack.name };
      this.runAction(stack.hostId, 'duo', action, params);
    },
    hostOnline(id) {
      const h = this.hosts.find(x => x.id === id);
      return !!h && h.status === 'online';
    },
    // dispatchSilent fires an action without opening the job modal; returns {jobId|approvalId} or null.
    async dispatchSilent(hostId, mod, action, params) {
      const opts = { method: 'POST' };
      if (params) { opts.headers = { 'Content-Type': 'application/json' }; opts.body = JSON.stringify(params); }
      const r = await fetch(`/api/v1/hosts/${hostId}/modules/${mod}/actions/${action}`, opts);
      if (!r.ok) return null;
      return r.json().catch(() => null);
    },
    // awaitJob polls a job until it reaches a terminal state, returning the snapshot (or null).
    async awaitJob(jobId, timeoutMs = 30000) {
      const terminal = ['succeeded', 'failed', 'timed_out', 'needs_sudo_password'];
      const start = Date.now();
      while (Date.now() - start < timeoutMs) {
        const r = await fetch(`/api/v1/jobs/${jobId}`);
        if (r.ok) {
          const j = await r.json();
          if (terminal.includes(j.state)) return j;
        }
        await new Promise(res => setTimeout(res, 300));
      }
      return null;
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
    // ---- updates ----
    // updateHosts merges the flat os/containers projections into one row per host so the
    // Updates page can show each host's package and container image updates together in its
    // own panel. Hosts appear if they report qup status or have any container image update.
    updateHosts() {
      const byId = new Map();
      for (const o of this.updates.os) {
        byId.set(o.hostId, { hostId: o.hostId, hostName: o.hostName, os: o, containers: [] });
      }
      for (const c of this.updates.containers) {
        let e = byId.get(c.hostId);
        if (!e) { e = { hostId: c.hostId, hostName: c.hostName, os: null, containers: [] }; byId.set(c.hostId, e); }
        e.containers.push(c);
      }
      const rows = this.sortByHost([...byId.values()]);
      for (const r of rows) r.containers = this.sortByHost(r.containers, x => x.stack + '/' + x.service);
      return rows;
    },
    shortDigest(d) {
      if (!d) return '';
      const h = String(d).replace(/^sha256:/, '');
      return h.length > 12 ? h.slice(0, 12) : h;
    },
    checkHost(hostId) {
      const h = this.hosts.find(x => x.id === hostId);
      if (!h) return;
      const mods = (h.modules || []).map(m => m.name);
      if (mods.includes('qup')) this.dispatchSilent(hostId, 'qup', 'check-updates');
      if (mods.includes('duo')) this.dispatchSilent(hostId, 'duo', 'check-updates');
    },
    checkAllUpdates() {
      for (const h of this.hosts) if (h.status === 'online') this.checkHost(h.id);
    },
    updateService(c) {
      this.runAction(c.hostId, 'duo', 'update', { stack: c.stack, service: c.service });
    },
    // updateAllHost updates every stack on a host that has a container image update. Each stack
    // is a separate job (labelled by stack) so they show in the queue indicator; the associate
    // serializes them per host. A whole-stack update pulls all its images and recreates.
    async updateAllHost(hu) {
      const stacks = [...new Set(hu.containers.map(c => c.stack))];
      for (const stack of stacks) {
        const out = await this.dispatchSilent(hu.hostId, 'duo', 'update', { stack });
        if (out && out.jobId) this.watchJob(out.jobId, 'update ' + stack);
        else if (out && out.approvalId) await this.refresh();
      }
    },
    openLogs(stack, service) {
      this.closeLogs();
      const title = service ? `${stack.name}/${service}` : stack.name;
      this.logView = { open: true, title, lines: [], es: null };
      const q = new URLSearchParams({ module: 'duo', stack: stack.name });
      if (service) q.set('service', service);
      const es = new EventSource(`/api/v1/hosts/${stack.hostId}/logs?${q.toString()}`);
      es.onmessage = (e) => { this.logView.lines.push(e.data); if (this.logView.lines.length > 500) this.logView.lines.shift(); };
      this.logView.es = es;
    },
    closeLogs() {
      if (this.logView.es) this.logView.es.close();
      this.logView = { open: false, title: '', lines: [], es: null };
    },
    async submitHost() {
      const body = { ...this.newHost, connPort: Number(this.newHost.connPort) || 0 };
      const r = await fetch('/api/v1/hosts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) { alert('enroll failed'); return; }
      const { jobId } = await r.json();
      const hostName = this.newHost.name;
      this.addHostOpen = false;
      this.newHost = { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false, connMode: 'manager_dial', connPort: null };
      this.page = 'hosts';
      await this.refresh();
      this.watchJob(jobId, 'enroll ' + hostName);
    },
  };
}
