// Job queue + docked log panel: the live job records, their SSE streaming/reconciliation, the
// panel resize handling, and the container log viewer.

// Bounds on the holding area for events that arrive before their job has a record (see
// bufferJobEvent). Dispatch-to-record is one HTTP round trip, so the window needs to be
// generous, not long; the caps stop another tab's activity accumulating here indefinitely.
const pendingTTL = 30000;
const maxPendingJobs = 50;
const maxPendingEvents = 1000;

export const jobs = {
  job: { id: '', label: '', state: '', progress: 0, log: [] }, // the job currently on screen
  jobs: [], // all active jobs (queued/running + briefly-settled), shown in the queue indicator
  jobPanelOpen: false, // whether the docked log panel is visible
  jobStick: true, // keep the job log pinned to the newest line until the user scrolls up
  jobPanelHeight: 0, // px override for the docked job panel (0 = CSS default of 33vh)
  logView: { open: false, title: '', lines: [], es: null, status: '' },

  // isTerminalJob reports whether a job state is final (no more events will come).
  isTerminalJob(s) { return s === 'succeeded' || s === 'failed' || s === 'timed_out'; },
  // isSettledJob adds the states that produce no further events without being job terminals: a
  // sudo hand-off is waiting on the user, and 'restarting' marks the manager self-update, whose
  // stream ends with the process. The panel treats all of them as "done with this one" so a
  // later job can take the foreground — otherwise a self-update pinned the panel for good.
  isSettledJob(s) { return this.isTerminalJob(s) || s === 'needs_sudo_password' || s === 'restarting'; },
  // showJob puts a job's record on screen without forcing the panel open — opening is lazy
  // (see the event handler) so a sudo hand-off or silent success doesn't flash the panel.
  showJob(rec) {
    this.job = rec;
    this.jobStick = true;
    // Swapping the panel to a different job's log fires a scroll event as the browser clamps
    // scrollTop to the new (shorter) content. Mark it programmatic so onJobScroll doesn't read
    // it as the user scrolling up and detach autoscroll — which would freeze the next job's log.
    this._autoScroll = true;
    this.$nextTick(() => this.scrollJobToBottom());
  },
  // scrollJobToBottom pins the log to the newest line and flags the resulting scroll event as
  // programmatic (consumed once by onJobScroll) so it isn't mistaken for a user detach.
  scrollJobToBottom() {
    const el = this.$refs.jobLog;
    if (!el) return;
    this._autoScroll = true;
    el.scrollTop = el.scrollHeight;
  },
  // selectJob is the user clicking a queue chip to bring that job's log to the front.
  selectJob(rec) { this.showJob(rec); this.jobPanelOpen = true; },
  // watchJob starts showing a job in the docked panel and returns a promise that resolves with
  // the job's final state once it settles (terminal, needs-sudo, or gone). Its progress/log/state
  // events arrive on the shared /api/v1/events feed and are applied by onJobEvent — there is NO
  // per-job connection, so any number of jobs can be watched at once without touching the
  // browser's per-origin connection cap. Callers that serialize on completion await this promise.
  // adoptJob creates (or updates) the panel record for a job, without attaching a completion
  // waiter. watchJob layers the promise on top; recoverJobs uses it bare, so re-adopting jobs
  // after a reload no longer registers resolvers nobody ever awaits.
  adoptJob(jobId, label, meta) {
    // One record per job so overlapping jobs don't cross-contaminate a shared log. Push once,
    // then hold the reactive proxy Alpine returns from find — mutating the raw pushed object
    // bypasses the proxy's set trap and the panel never repaints. hostId/module/action (meta)
    // let page code tell whether a host has work in flight (see updates.js hostUpdating) so a
    // per-host loading spinner can survive a page refresh.
    let rec = this.jobs.find(j => j.id === jobId);
    if (!rec) {
      this.jobs.push({
        id: jobId, label: label || ('job ' + String(jobId).slice(0, 6)), state: 'queued', progress: 0, log: [],
        hostId: (meta && meta.hostId) || '', module: (meta && meta.module) || '', action: (meta && meta.action) || '',
        sawEvent: false, retiring: false,
      });
      rec = this.jobs.find(j => j.id === jobId);
    } else if (meta) {
      rec.hostId = meta.hostId || rec.hostId;
      rec.module = meta.module || rec.module;
      rec.action = meta.action || rec.action;
    }
    // Drain events the feed delivered before this record existed. The manager creates a job and
    // hands it to the associate before the dispatch response gets back here, so a job's first
    // log lines — and, for a fast one, its terminal state — routinely arrive while there is
    // still nothing to apply them to. They used to be discarded, which lost the head of every
    // log and left quick jobs waiting on a completion that had already happened.
    const buf = this._pendingEvents && this._pendingEvents[jobId];
    if (buf) {
      delete this._pendingEvents[jobId];
      for (const ev of buf.events) this.applyJobEvent(rec, ev);
    }
    return rec;
  },
  watchJob(jobId, label, meta) {
    const rec = this.adoptJob(jobId, label, meta);
    // Adopt the job on screen when nothing live is showing (or the panel is closed); otherwise
    // leave the current job up and let this one wait in the queue indicator.
    if (!this.jobPanelOpen || !this.job.id || this.isSettledJob(this.job.state)) this.showJob(rec);
    // Already settled — a very fast job, or one whose whole life arrived in the drain above.
    // Re-run the panel decision now that it is the job on screen, then resolve.
    if (this.isSettledJob(rec.state)) { this.finishJob(rec, rec.state); return Promise.resolve(rec.state); }
    (this._jobWaiters ||= {});
    // A list, not a single slot: when two callers watch the same job, the one that registered
    // first used to be overwritten and its await never resolved, stranding the loading state
    // it gated (see updates.js runHostUpdates, whose finally then never ran).
    return new Promise(res => { (this._jobWaiters[jobId] ||= []).push(res); });
  },
  // onJobEvent routes one job event from the multiplexed feed. Events for a job with no record
  // yet are held briefly rather than dropped — see adoptJob's drain.
  onJobEvent(ev) {
    if (!ev || !ev.jobId) return;
    const r = this.jobs.find(j => j.id === ev.jobId);
    if (r) { this.applyJobEvent(r, ev); return; }
    this.bufferJobEvent(ev);
  },
  // bufferJobEvent parks an event for a job this session hasn't started tracking yet. Entries
  // expire so activity from another tab (whose jobs we never adopt) can't pile up here.
  bufferJobEvent(ev) {
    const now = Date.now();
    const buf = (this._pendingEvents ||= {});
    for (const id of Object.keys(buf)) {
      if (now - buf[id].at > pendingTTL) delete buf[id];
    }
    if (!buf[ev.jobId] && Object.keys(buf).length >= maxPendingJobs) return;
    const entry = (buf[ev.jobId] ||= { at: now, events: [] });
    if (entry.events.length >= maxPendingEvents) entry.events.shift();
    entry.events.push(ev);
  },
  // applyJobEvent folds one event into a job's record.
  applyJobEvent(r, ev) {
    r.sawEvent = true;
    if (ev.kind === 'log' && ev.message) {
      r.log.push(ev.message);
      if (r.state === 'queued') r.state = 'running';
      this.adoptIfIdle(r);
      // Only steal focus/scroll for the job actually on screen. jobStick is driven by the user's
      // own scrolling (see onJobScroll), so a fast burst can't stop the autoscroll.
      if (this.job.id === r.id) {
        this.jobPanelOpen = true;
        if (this.jobStick) this.$nextTick(() => this.scrollJobToBottom());
      }
    } else if (ev.kind === 'progress') {
      r.progress = ev.progress;
      if (r.state === 'queued') r.state = 'running';
      this.adoptIfIdle(r);
      if (this.job.id === r.id) this.jobPanelOpen = true;
    } else if (ev.kind === 'state') {
      r.state = ev.state;
      if (ev.state === 'needs_sudo_password' || this.isTerminalJob(ev.state)) {
        this.refreshSoon();
        this.finishJob(r, ev.state);
        this.settleJob(r.id, ev.state);
      }
    }
  },
  // settleJob resolves every watchJob promise for a job, once.
  settleJob(jobId, state) {
    const ws = this._jobWaiters && this._jobWaiters[jobId];
    if (!ws) return;
    delete this._jobWaiters[jobId];
    for (const w of ws) w(state);
  },
  // recoverJobs re-adopts jobs still running on the manager after a page (re)load, so refreshing
  // mid-run doesn't orphan them: each reappears in the queue/panel and its completion is watched
  // again. The live feed is forward-only, so log lines printed before the reload aren't replayed —
  // but new output, progress, and the final state all resume.
  async recoverJobs() {
    let list;
    try {
      const r = await fetch('/api/v1/jobs');
      if (!r.ok) return;
      list = await r.json();
    } catch { return; }
    for (const j of (list || [])) {
      if (this.isTerminalJob(j.state)) continue; // finished already; nothing to watch
      if (this.jobs.some(x => x.id === j.id)) continue; // already tracked this session
      const rec = this.adoptJob(j.id, `${j.module} ${j.action}`, { hostId: j.hostId, module: j.module, action: j.action });
      // j.state is a snapshot from before this fetch returned, so only seed from it when
      // nothing newer has landed. Writing it unconditionally rolled a job that finished during
      // the fetch back to 'running', where it stayed — its terminal event was already spent.
      if (!rec.sawEvent) rec.state = j.state;
    }
  },
  // reconcileJobs is the safety net for a dropped/reconnected event feed: it asks the job store
  // for the truth about every job still being awaited so a terminal state missed during the gap
  // can't leave a promise (and the loading spinner it gates) hanging until a page reload.
  async reconcileJobs() {
    if (!this._jobWaiters || !Object.keys(this._jobWaiters).length) return;
    let list;
    try {
      const r = await fetch('/api/v1/jobs');
      if (!r.ok) return;
      list = await r.json();
    } catch { return; }
    const byId = new Map((list || []).map(j => [j.id, j]));
    for (const id of Object.keys(this._jobWaiters)) {
      const j = byId.get(id);
      if (!j) { // pruned before we observed its end: retire the chip without faking a failure.
        this.jobs = this.jobs.filter(x => x.id !== id);
        if (this.job.id === id) this.jobPanelOpen = false;
        this.settleJob(id, 'gone');
        continue;
      }
      if (this.isSettledJob(j.state)) {
        const r = this.jobs.find(x => x.id === id);
        if (r) { r.state = j.state; this.finishJob(r, j.state); }
        this.settleJob(id, j.state);
      }
    }
  },
  // jobTitle labels a job chip/header with its action and, when the host is known, the host it
  // runs on — so the queue reads "qup apply · web01" instead of a generic label. Callers that
  // already fold the host into the label (enroll/uninstall/…) pass no hostId, so nothing doubles.
  jobTitle(j) { return j && j.hostId ? `${j.label} · ${this.hostName(j.hostId)}` : (j ? j.label : ''); },
  // adoptIfIdle brings a job to the front when nothing live is showing (first run, or the
  // previously shown job has finished), so a job that was queued behind another surfaces on
  // its own once it starts producing output.
  adoptIfIdle(rec) {
    if (this.job.id !== rec.id && (!this.job.id || this.isSettledJob(this.job.state))) this.showJob(rec);
  },
  // finishJob settles a terminal job: decide whether the panel stays up, then retire it from
  // the queue indicator. A still-queued job takes over the panel later via adoptIfIdle.
  finishJob(rec, state) {
    if (this.job.id === rec.id) {
      // A sudo prompt or a clean output-less success hands off elsewhere — keep the panel
      // closed. A failure, or a success with output, is worth showing.
      this.jobPanelOpen = !(state === 'needs_sudo_password' || (state === 'succeeded' && rec.log.length === 0));
    }
    // The panel decision above re-runs whenever a job settles again — a reconcile after the
    // live event, or a drain that completed before the job reached the screen. Retiring must
    // not, or each pass would arm another timer against the same record.
    if (rec.retiring) return;
    rec.retiring = true;
    // Drop it from the indicator: sudo hand-offs immediately, others after a beat so the user
    // sees them settle. If it's still the shown job, the log stays up until the panel closes.
    const retire = () => { this.jobs = this.jobs.filter(j => j.id !== rec.id); };
    if (state === 'needs_sudo_password') retire();
    else this._later(retire, 4000);
  },
  // onJobScroll re-arms or releases autoscroll from the user's scroll position: at (or near)
  // the bottom re-pins; scrolling up to read history releases the pin. Programmatic scrolls
  // land at the bottom, so they simply keep jobStick true.
  onJobScroll(e) {
    // Consume one scroll event caused by our own programmatic scroll (autoscroll or job switch)
    // so it isn't misread as a user detaching from the bottom.
    if (this._autoScroll) { this._autoScroll = false; return; }
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
  openLogs(stack, service) {
    this.closeLogs();
    const title = service ? `${stack.name}/${service}` : stack.name;
    this.logView = { open: true, title, lines: [], es: null, status: 'Connecting…' };
    const q = new URLSearchParams({ module: 'duo', stack: stack.name });
    if (service) q.set('service', service);
    const es = new EventSource(`/api/v1/hosts/${stack.hostId}/logs?${q.toString()}`);
    let failures = 0;
    es.onopen = () => { failures = 0; this.logView.status = ''; };
    es.onmessage = (e) => {
      failures = 0;
      this.logView.status = '';
      this.logView.lines.push(e.data);
      if (this.logView.lines.length > 500) this.logView.lines.shift();
    };
    // Without a handler here the stream can die — host offline, or the manager refusing the
    // open with 409 — and EventSource retries forever in silence, leaving an empty modal and
    // no hint that anything is wrong. Say what happened, and stop after repeated failures.
    es.onerror = () => {
      if (++failures >= 5) {
        es.close();
        this.logView.status = 'Log stream disconnected — close and reopen to retry.';
        return;
      }
      this.logView.status = 'Log stream interrupted — reconnecting…';
    };
    this.logView.es = es;
  },
  closeLogs() {
    if (this.logView && this.logView.es) this.logView.es.close();
    this.logView = { open: false, title: '', lines: [], es: null, status: '' };
  },
};
