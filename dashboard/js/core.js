// Core dashboard state: session/auth lifecycle, the periodic refresh, and shared helpers used
// across every page. Merged into the app() component in main.js.
export const core = {
  page: 'overview',
  navOpen: false, // mobile navbar-burger toggle (collapsed by default on narrow viewports)
  overview: {},
  hosts: [],
  ready: false,
  needsLogin: false,
  authUser: '',
  login: { username: '', password: '' },
  loginError: '',
  settings: { logLevel: 'info', defaultTimezone: '' },
  instanceId: '', // manager process marker; a change means it restarted underneath us
  stale: false, // manager restarted, so this page's session/state is no longer valid

  async init() {
    // Tear the live feed down cleanly when the page goes away (refresh/close/navigate) so the
    // server frees this client's SSE subscription promptly instead of waiting on a socket timeout.
    // pagehide covers the bfcache case that a plain unload listener misses.
    window.addEventListener('pagehide', () => this.disconnect());
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
    this.connectEvents();
    // Re-adopt any jobs still running server-side (e.g. after a refresh mid-run) so they aren't
    // orphaned: their progress resumes in the panel and their completion is observed again.
    this.recoverJobs();
    // Poll the manager's instance marker (public endpoint, works in auth and open mode) so a
    // restart underneath us surfaces the stale banner even when the SSE stream can't reconnect.
    clearInterval(this._instanceTimer);
    this._instanceTimer = setInterval(() => this.checkInstance(), 10000);
  },
  // disconnect closes this client's streams and timers so nothing keeps ticking or holding a
  // server subscription after the page is gone (or before start() reopens them).
  disconnect() {
    if (this._events) { this._events.close(); this._events = null; }
    this.closeLogs();
    clearTimeout(this._refreshTimer);
    clearInterval(this._instanceTimer);
  },
  // connectEvents opens the single multiplexed live feed that drives the whole UI. The manager
  // publishes every kind of update onto this one stream (job progress/log/state, host changes,
  // approvals, sudo prompts, audit, tasks), so one browser connection covers everything — no
  // per-job stream and no per-request polling, which on plain HTTP/1.1 would otherwise exhaust
  // the ~6-connection-per-origin cap. Job events update the job records in place (see
  // onJobEvent); every other kind coalesces into a single debounced refresh, so a burst of
  // events costs one reload instead of one full refresh per message.
  connectEvents() {
    if (this._events) this._events.close();
    const es = new EventSource('/api/v1/events');
    this._events = es;
    es.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch { return; }
      if (msg.type === 'job_event') { this.onJobEvent(msg.payload); return; }
      this.refreshSoon();
    };
    // A dropped/reconnected feed may have skipped a watched job's terminal event; reconcile the
    // in-flight waiters against the job store so their promises (and the loading spinners they
    // gate) can't hang after a gap. EventSource auto-reconnects on its own.
    es.onerror = () => { this.reconcileJobs(); };
  },
  // refreshSoon coalesces a burst of state-change events into one refresh on the next tick.
  refreshSoon() {
    clearTimeout(this._refreshTimer);
    this._refreshTimer = setTimeout(() => this.refresh(), 250);
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
  async saveSettings() {
    await fetch('/api/v1/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.settings) });
    this.refresh();
  },
  hostName(id) { const h = this.hosts.find(x => x.id === id); return h ? h.name : id; },
  // ipKey turns an IP into a zero-padded string so a plain string compare orders octets
  // numerically (so .10 sorts after .9).
  ipKey(ip) { return (ip || '').split('.').map(o => String(parseInt(o, 10) || 0).padStart(3, '0')).join('.'); },
  hostOnline(id) {
    const h = this.hosts.find(x => x.id === id);
    return !!h && h.status === 'online';
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
        if (sv.status === 'stopped' || sv.status === 'exited' || this.svcUpdate(sv)) return 'warn';
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
};
