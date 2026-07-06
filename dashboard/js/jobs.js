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
    // Reconcile on stream error. A finished job's stream is closed by the server; if we never
    // saw its terminal state (it settled and was pruned before we subscribed, or the connection
    // dropped mid-flight), EventSource would silently auto-reconnect forever and the chip would
    // stick in the queue until a full page reload. So on error, ask the job store for the truth:
    // gone or terminal means retire it; still-live means let EventSource reconnect and resume.
    es.onerror = () => {
      if (this.isTerminalJob(rec.state)) { es.close(); return; }
      fetch(`/api/v1/jobs/${jobId}`)
        .then(r => r.ok ? r.json() : { state: 'gone' })
        .then(j => {
          if (j.state === 'gone') {
            // No longer in the store: finished and pruned before we observed it. Drop the chip
            // without faking a failure, and close the panel if it was the one on screen.
            es.close();
            this.jobs = this.jobs.filter(x => x.id !== jobId);
            if (this.job.id === jobId) this.jobPanelOpen = false;
            this.refresh();
          } else if (j.state === 'needs_sudo_password' || this.isTerminalJob(j.state)) {
            es.close();
            this.refresh();
            this.finishJob(rec, j.state);
          }
          // still queued/running: leave es alone so it reconnects and resumes streaming.
        })
        .catch(() => {});
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
