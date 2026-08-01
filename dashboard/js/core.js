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
  staleReason: '', // 'restart' | 'auth' — which of the two invalidated this page
  refreshError: '', // set when a refresh couldn't read part of the state (showing older values)

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
    this.stale = false;
    this.staleReason = '';
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
    this.closeManagerUpdateStream();
    clearTimeout(this._refreshTimer);
    clearInterval(this._instanceTimer);
    clearInterval(this._staleTimer);
    for (const id of (this._timers || [])) clearTimeout(id);
    this._timers = new Set();
  },
  // _later is setTimeout that disconnect() can cancel, so deferred work (retiring a finished
  // job chip, say) doesn't fire against a torn-down page.
  _later(fn, ms) {
    const timers = (this._timers ||= new Set());
    const id = setTimeout(() => { timers.delete(id); fn(); }, ms);
    timers.add(id);
    return id;
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
      // The manager sends resync when this subscriber's buffer overflowed, or its reconnect
      // landed past the replay window: events were dropped, so re-read everything rather than
      // keep running on a feed that may have lost a host change or a job's terminal state.
      if (msg.type === 'resync') { this.refresh(); this.reconcileJobs(); return; }
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
    if (this.stale || this.needsLogin) return;
    try {
      const r = await fetch('/api/v1/auth/session');
      if (!r.ok) return;
      const s = await r.json();
      if (this.instanceId && s.instance && s.instance !== this.instanceId) this.markStale('restart');
      else if (s.authRequired && !s.authenticated) this.markStale('auth');
    } catch (e) { /* manager down mid-restart; next tick retries */ }
  },
  // markStale freezes this page: nothing it still holds open (feed, timers, log streams) can
  // produce trustworthy state any more, and leaving them running only generates failed requests
  // behind the banner.
  markStale(reason) {
    if (this.stale) return;
    this.stale = true;
    this.staleReason = reason;
    this.disconnect();
  },
  // The two ways a page goes stale need different words: a restart lost the manager's
  // in-memory state, an expiry only ended this session.
  staleMessage() {
    return this.staleReason === 'auth'
      ? 'Your session ended. Sign in again to reconnect.'
      : 'The manager restarted. This page is out of date — sign in again to reconnect.';
  },
  reloadForLogin() { window.location.reload(); },
  async doLogin() {
    const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.login) });
    if (!r.ok) { this.loginError = 'Invalid credentials'; return; }
    const data = await r.json().catch(() => ({}));
    this.authUser = data.username || this.login.username;
    this.loginError = ''; this.login.password = ''; this.needsLogin = false;
    // init() may have bailed before reading the instance marker (session endpoint unreachable
    // at load). Without it checkInstance can never detect a restart, so pick it up now.
    if (!this.instanceId) {
      try { this.instanceId = (await (await fetch('/api/v1/auth/session')).json()).instance || ''; }
      catch (e) { /* the poller will keep trying */ }
    }
    this.start();
  },
  async logout() {
    await fetch('/api/v1/auth/logout', { method: 'POST' });
    // Tear the live plumbing down before flipping to the login form. Left running, the instance
    // poller sees the now-unauthenticated session on its next tick and raises the stale banner —
    // so a deliberate logout announced "The manager restarted" over the login page.
    this.disconnect();
    this.authUser = ''; this.needsLogin = true;
    this.stale = false; this.staleReason = ''; this.refreshError = '';
  },
  // refresh re-reads every page's state. It is single-flight: two callers never run overlapping
  // passes, which used to interleave and let an older response land last and overwrite newer
  // status. A caller that arrives while a pass is running gets a *fresh* pass queued behind it
  // (not the in-flight one, whose data predates the change it is refreshing for), so
  // `await refresh()` always resolves on state read after the caller asked for it.
  refresh() {
    if (this._refreshing) {
      this._refreshQueued ||= this._refreshing.then(() => this.refresh());
      return this._refreshQueued;
    }
    const p = this._doRefresh().finally(() => { this._refreshing = null; this._refreshQueued = null; });
    this._refreshing = p;
    return p;
  },
  // _load reads one endpoint, falling back to the value already on screen. Keeping the last
  // known-good value means one failing endpoint degrades to a stale panel instead of a blank
  // one — and, unlike the previous single try/catch around every fetch, it no longer aborts
  // each remaining read, which silently froze the whole dashboard on one bad response.
  async _load(path, current, failed) {
    try {
      const r = await fetch(path);
      if (!r.ok) { failed.push(path); return current; }
      return await r.json();
    } catch (e) { failed.push(path); return current; }
  },
  async _doRefresh() {
    // Sequential on purpose: the live feed already holds one of the browser's ~6 connections
    // per origin, and firing all of these at once alongside job polls exhausts the rest.
    const failed = [];
    const quiet = []; // failures not worth a banner (endpoint is credential-gated)
    this.overview = await this._load('/api/v1/overview', this.overview, failed);
    this.hosts = await this._load('/api/v1/hosts', this.hosts, failed);
    this.services = await this._load('/api/v1/services', this.services, failed);
    this.updates = await this._load('/api/v1/updates', this.updates, failed);
    this.tasks = await this._load('/api/v1/tasks', this.tasks, failed);
    this.approvals = await this._load('/api/v1/approvals', this.approvals, failed);
    this.sudoPrompts = await this._load('/api/v1/sudo', this.sudoPrompts, failed);
    await this._loadAudit();
    this.settings = await this._load('/api/v1/settings', this.settings, failed);
    this.tokens = await this._load('/api/v1/auth/tokens', this.tokens, quiet);
    this.refreshError = failed.length
      ? `Couldn't refresh ${failed.length === 1 ? 'one part' : failed.length + ' parts'} of the dashboard — showing the last values read successfully.`
      : '';
  },
  // Audit has its own error line on the audit page (it is permission-gated per credential), so
  // it reports there rather than through the general refresh banner.
  async _loadAudit() {
    try {
      const ar = await fetch('/api/v1/audit');
      if (ar.ok) {
        this.audit = await ar.json();
        this.auditError = '';
        if (this.auditPage > this.auditPages()) this.auditPage = this.auditPages();
        return;
      }
      this.audit = [];
      this.auditError = ar.status === 403 ? 'Audit access not permitted for this credential.' : 'Failed to load audit log.';
    } catch (e) {
      this.audit = [];
      this.auditError = 'Failed to load audit log.';
    }
  },
  async saveSettings() {
    const r = await fetch('/api/v1/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.settings) });
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      alert('Failed to save settings: ' + ((e.error && e.error.message) || r.status));
    }
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
