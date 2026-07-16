// Job queue + docked log panel: the live job records, their SSE streaming/reconciliation, the
// panel resize handling, and the container log viewer.
export const jobs = {
  job: { id: '', label: '', state: '', progress: 0, log: [] }, // the job currently on screen
  jobs: [], // all active jobs (queued/running + briefly-settled), shown in the queue indicator
  jobPanelOpen: false, // whether the docked log panel is visible
  jobStick: true, // keep the job log pinned to the newest line until the user scrolls up
  jobPanelHeight: 0, // px override for the docked job panel (0 = CSS default of 33vh)
  logView: { open: false, title: '', lines: [], es: null },

  // isTerminalJob reports whether a job state is final (no more events will come).
  isTerminalJob(s) { return s === 'succeeded' || s === 'failed' || s === 'timed_out'; },
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
  watchJob(jobId, label, meta) {
    // One record per job so overlapping jobs don't cross-contaminate a shared log. Reuse an
    // existing record if a feed event already created it (a race where the first event lands
    // before this call). Push once, then hold the reactive proxy Alpine returns from find —
    // mutating the raw pushed object bypasses the proxy's set trap and the panel never repaints.
    // hostId/module/action (meta) let page code tell whether a host has work in flight (see
    // updates.js hostUpdating) so a per-host loading spinner can survive a page refresh.
    let rec = this.jobs.find(j => j.id === jobId);
    if (!rec) {
      this.jobs.push({
        id: jobId, label: label || ('job ' + String(jobId).slice(0, 6)), state: 'queued', progress: 0, log: [],
        hostId: (meta && meta.hostId) || '', module: (meta && meta.module) || '', action: (meta && meta.action) || '',
      });
      rec = this.jobs.find(j => j.id === jobId);
    } else if (meta) {
      rec.hostId = meta.hostId || rec.hostId;
      rec.module = meta.module || rec.module;
      rec.action = meta.action || rec.action;
    }
    // Adopt the job on screen when nothing live is showing (or the panel is closed); otherwise
    // leave the current job up and let this one wait in the queue indicator.
    if (!this.jobPanelOpen || !this.job.id || this.isTerminalJob(this.job.state)) this.showJob(rec);
    // Already settled (e.g. a very fast job): resolve immediately.
    if (rec.state === 'needs_sudo_password' || this.isTerminalJob(rec.state)) return Promise.resolve(rec.state);
    (this._jobWaiters ||= {});
    return new Promise(res => { this._jobWaiters[jobId] = res; });
  },
  // onJobEvent applies one job event from the multiplexed feed to the matching record. Only jobs
  // with a record (created by watchJob) are tracked; events for any other job are ignored so
  // background or other-tab activity doesn't hijack this session's panel.
  onJobEvent(ev) {
    const r = this.jobs.find(j => j.id === ev.jobId);
    if (!r) return;
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
  // settleJob resolves the watchJob promise for a job once (if anything is awaiting it).
  settleJob(jobId, state) {
    const w = this._jobWaiters && this._jobWaiters[jobId];
    if (w) { delete this._jobWaiters[jobId]; w(state); }
  },
  // recoverJobs re-adopts jobs still running on the manager after a page (re)load, so refreshing
  // mid-run doesn't orphan them: each reappears in the queue/panel and its completion is watched
  // again. The live feed is forward-only, so log lines printed before the reload aren't replayed —
  // but new output, progress, and the final state all resume.
  async recoverJobs() {
    let list;
    try { list = await (await fetch('/api/v1/jobs')).json(); } catch { return; }
    for (const j of (list || [])) {
      if (this.isTerminalJob(j.state)) continue; // finished already; nothing to watch
      if (this.jobs.some(x => x.id === j.id)) continue; // already tracked this session
      this.watchJob(j.id, `${j.module} ${j.action}`, { hostId: j.hostId, module: j.module, action: j.action });
      const r = this.jobs.find(x => x.id === j.id);
      if (r) r.state = j.state; // reflect real state now (watchJob seeds 'queued')
    }
  },
  // reconcileJobs is the safety net for a dropped/reconnected event feed: it asks the job store
  // for the truth about every job still being awaited so a terminal state missed during the gap
  // can't leave a promise (and the loading spinner it gates) hanging until a page reload.
  async reconcileJobs() {
    if (!this._jobWaiters || !Object.keys(this._jobWaiters).length) return;
    let list;
    try { list = await (await fetch('/api/v1/jobs')).json(); } catch { return; }
    const byId = new Map((list || []).map(j => [j.id, j]));
    for (const id of Object.keys(this._jobWaiters)) {
      const j = byId.get(id);
      if (!j) { // pruned before we observed its end: retire the chip without faking a failure.
        this.jobs = this.jobs.filter(x => x.id !== id);
        if (this.job.id === id) this.jobPanelOpen = false;
        this.settleJob(id, 'gone');
        continue;
      }
      if (j.state === 'needs_sudo_password' || this.isTerminalJob(j.state)) {
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
};
