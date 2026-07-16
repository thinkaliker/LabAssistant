// Updates page: the os/container update projection, the generic silent-dispatch/await helpers,
// and the check/apply flows for OS packages and container images.
export const updates = {
  updates: { os: [], containers: [] },
  checkingHosts: [], // host ids with an in-flight check-updates, for button loading state
  updatingHosts: [], // host ids with an in-flight apply/update, for button loading state

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
  // hostUpdating reports whether a host has OS/container update work in flight: either a local
  // apply/update loop this session (updatingHosts) OR a matching job still running on the manager.
  // The job check is what makes the button spinner survive a page refresh — recoverJobs re-adopts
  // in-flight jobs into this.jobs, so a reload mid-apply still shows the host as busy until it
  // finishes. hostChecking is the same idea for check-updates jobs.
  hostUpdating(id) {
    return this.updatingHosts.includes(id) ||
      this.jobs.some(j => j.hostId === id && !this.isTerminalJob(j.state) &&
        ((j.module === 'qup' && j.action === 'apply') || (j.module === 'duo' && j.action === 'update')));
  },
  hostChecking(id) {
    return this.checkingHosts.includes(id) ||
      this.jobs.some(j => j.hostId === id && !this.isTerminalJob(j.state) && j.action === 'check-updates');
  },
  shortDigest(d) {
    if (!d) return '';
    const h = String(d).replace(/^sha256:/, '');
    return h.length > 12 ? h.slice(0, 12) : h;
  },
  // A real "sha256:<64 hex>" digest. Mirrors the manager guard so phantom updates from stale
  // associate reports (bogus latest like "Name: ...") never render or get counted.
  isDigest(d) { return /^sha256:[0-9a-f]{64}$/.test(String(d || '')); },
  // A service has a genuine, actionable update only when both digests are real.
  svcUpdate(sv) { return !!sv.updateAvailable && this.isDigest(sv.currentDigest) && this.isDigest(sv.latestDigest); },
  // checkHost runs qup/duo check-updates and waits for the jobs to finish before refreshing,
  // so freshly-found updates actually appear. Without the await+refresh the fire-and-forget
  // dispatch completed on the associate but the page never re-read the module status, so the
  // button looked dead. checkingHosts drives the button's loading state.
  async checkHost(hostId) {
    const h = this.hosts.find(x => x.id === hostId);
    if (!h || this.hostChecking(hostId)) return;
    this.checkingHosts.push(hostId);
    try {
      const mods = (h.modules || []).map(m => m.name);
      const dispatched = [];
      if (mods.includes('qup')) dispatched.push(this.dispatchSilent(hostId, 'qup', 'check-updates'));
      if (mods.includes('duo')) dispatched.push(this.dispatchSilent(hostId, 'duo', 'check-updates'));
      const outs = await Promise.all(dispatched);
      await Promise.all(outs.map(o => (o && o.jobId) ? this.awaitJob(o.jobId) : null));
      await this.refresh();
    } finally {
      this.checkingHosts = this.checkingHosts.filter(x => x !== hostId);
    }
  },
  async checkAllUpdates() {
    await Promise.all(this.hosts.filter(h => h.status === 'online').map(h => this.checkHost(h.id)));
  },
  // runUpdate dispatches one destructive apply/update action, waits for the job to reach a
  // terminal state, and reports whether it queued an approval instead of running. Both apply
  // (qup) and update (duo) are destructive, so a policy can gate them behind an approval; in
  // that case dispatchSilent returns {approvalId} and there is no job to await. Returns
  // { approval } so the caller can surface the pending-approvals banner once at the end.
  async runUpdate(hostId, mod, action, params) {
    const out = await this.dispatchSilent(hostId, mod, action, params);
    if (out && out.jobId) {
      // Stream the job into the docked panel so its progress/log is visible, and await the same
      // stream so this action's slot in runHostUpdates' serial loop completes before the next.
      // watchJob's returned promise resolves off the SSE's terminal event — no second polling
      // channel. On plain HTTP/1.1 the browser caps connections per origin (~6); a poll loop on
      // top of the long-lived SSE used to exhaust the pool when several jobs ran at once, so
      // later fetches hung and the loading spinner never cleared. One channel per job avoids it.
      const label = params && params.stack ? `${mod} ${action} ${params.stack}` : `${mod} ${action}`;
      await this.watchJob(out.jobId, label, { hostId, module: mod, action });
      return { approval: false };
    }
    if (out && out.approvalId) return { approval: true };
    return { approval: false };
  },
  // runHostUpdates runs a set of update actions for one host with a shared loading state, then
  // refreshes so applied updates drop out of the list. If any action was gated behind an
  // approval, it scrolls the pending-approvals banner into view so the user isn't left guessing.
  async runHostUpdates(hostId, actions) {
    if (this.hostUpdating(hostId)) return;
    this.updatingHosts.push(hostId);
    try {
      let approval = false;
      for (const a of actions) {
        const r = await this.runUpdate(hostId, a.mod, a.action, a.params);
        approval = approval || r.approval;
      }
      await this.refresh();
      if (approval) window.scrollTo({ top: 0, behavior: 'smooth' });
    } finally {
      this.updatingHosts = this.updatingHosts.filter(x => x !== hostId);
    }
  },
  // updateService updates a single compose service.
  updateService(c) {
    return this.runHostUpdates(c.hostId, [{ mod: 'duo', action: 'update', params: { stack: c.stack, service: c.service } }]);
  },
  // updateContainers updates every stack on a host that has a container image update. Each stack
  // is one action (a whole-stack update pulls all its images and recreates); the associate
  // serializes them per host.
  updateContainers(hu) {
    const stacks = [...new Set(hu.containers.map(c => c.stack))];
    return this.runHostUpdates(hu.hostId, stacks.map(stack => ({ mod: 'duo', action: 'update', params: { stack } })));
  },
  // updateHostPackages applies the host's pending OS package updates (qup).
  updateHostPackages(hu) {
    return this.runHostUpdates(hu.hostId, [{ mod: 'qup', action: 'apply' }]);
  },
  // applyAllUpdates does everything for one host: apply OS packages (qup) then update every
  // container stack (duo), in one shared loading state.
  applyAllUpdates(hu) {
    const actions = [];
    if (hu.os && hu.os.count > 0) actions.push({ mod: 'qup', action: 'apply' });
    const stacks = [...new Set(hu.containers.map(c => c.stack))];
    for (const stack of stacks) actions.push({ mod: 'duo', action: 'update', params: { stack } });
    return this.runHostUpdates(hu.hostId, actions);
  },
};
