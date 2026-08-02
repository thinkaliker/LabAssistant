// Hosts page: list ordering, per-host expansion, module config, add/edit/uninstall/revive flows.

// How long a credential retry is given to fail before it is assumed to have got past the login.
// A rejected login comes back well inside this — the dial is refused before a byte is uploaded —
// so anything still running afterwards is into the binary upload and service restart, which is
// work worth backgrounding rather than making the operator watch.
const credGraceMs = 2500;

export const hosts = {
  expanded: null,
  hostSort: 'name', // 'name' | 'ip' — how the Hosts list is ordered
  addHostOpen: false,
  // Edit Host modal. editHost holds the working copy; editHostOrig captures the values at open
  // time so we can detect changes to the associate-baked fields (connMode/connPort) and warn.
  editHostOpen: false,
  editHostId: null,
  editHost: { name: '', ip: '', sshUser: '', tailscale: false, connMode: 'manager_dial', connPort: null },
  editHostOrig: { connMode: 'manager_dial', connPort: null },
  newHost: { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false, connMode: 'manager_dial', connPort: null },
  cfg: { open: false, hostId: '', module: '', fields: [], values: {} },
  uninstall: { open: false, hostId: '', hostName: '', online: false, sshUser: '', sshPassword: '' },
  revive: { open: false, hostId: '', hostName: '', sshUser: '', sshPassword: '' },
  upgrade: { open: false, hostId: '', hostName: '', sshUser: '', sshPassword: '' },
  // Bulk associate upgrade. The first pass carries no credentials at all: it relies on the
  // manager's SSH key/agent and each host's stored user. Hosts that fail on credentials are
  // collected into credQueue and asked about one at a time, because a fleet does not
  // necessarily share one login and a single password box would be a lie.
  upgradeAll: { open: false, busy: false, error: '' },
  credQueue: [], // [{hostId, hostName, authRequired, error}] awaiting a retry, most fixable first
  credPrompt: {
    open: false, hostId: '', hostName: '', sshUser: '', origUser: '', sshPassword: '',
    authRequired: false, jobError: '', error: '', busy: false, remaining: 0,
  },

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
  async openConfig(hostId, m) {
    this.cfg = { open: true, hostId, module: m.name, fields: [], values: {} };
    const resp = await fetch(`/api/v1/hosts/${hostId}/modules/${m.name}/config`);
    if (!resp.ok) { this.cfg.open = false; alert('Could not read module config: ' + resp.status); return; }
    const r = await resp.json();
    const props = (r.schema && JSON.parse(typeof r.schema === 'string' ? r.schema : JSON.stringify(r.schema)).properties) || {};
    this.cfg.fields = Object.entries(props).map(([key, v]) => ({ key, title: v.title, secret: !!v.secret }));
    this.cfg.values = r.config || {};
  },
  async saveModuleConfig() {
    const r = await fetch(`/api/v1/hosts/${this.cfg.hostId}/modules/${this.cfg.module}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.cfg.values) });
    // Closing regardless read as success even when the save was rejected.
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      alert('save failed: ' + ((e.error && e.error.message) || r.status));
      return;
    }
    this.cfg.open = false;
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
  // associateStale reports that a host runs different associate code than the manager would
  // deploy — i.e. it is missing whatever shipped with the manager since.
  //
  // The comparison prefers associateCodeId, a fingerprint of the associate's own source, over
  // the commit. Comparing commits marked every host stale after any commit at all, including
  // dashboard-only ones, which made the banner permanent and therefore ignorable. The commit
  // is still the fallback for a host whose associate predates the fingerprint: it reports
  // none, and an unknown build has to be assumed stale until one upgrade stamps it.
  //
  // Both sides of whichever pair is used must be known: an offline host reports nothing, and
  // a manager with an unstamped associate binary has nothing to compare. Neither is evidence.
  //
  // Mirrors build.AssociateStale in the manager, which is what the bulk upgrade selects on.
  associateStale(h) {
    const o = this.overview || {};
    if (o.associateCodeId && h.associateCodeId) return h.associateCodeId !== o.associateCodeId;
    if (o.associateBuild && h.associateVersion) return h.associateVersion !== o.associateBuild;
    return false;
  },
  // staleAssociates counts hosts needing an upgrade, for the banner on every page. The
  // manager's own count is authoritative (it is what upgrade-stale acts on); the client-side
  // tally is the fallback for an overview that has not loaded yet.
  staleAssociates() {
    const o = this.overview || {};
    if (typeof o.staleAssociates === 'number') return o.staleAssociates;
    return this.hosts.filter(h => this.associateStale(h)).length;
  },
  openUpgrade(h) {
    this.upgrade = { open: true, hostId: h.id, hostName: h.name, sshUser: h.sshUser || '', sshPassword: '' };
  },
  // submitUpgrade pushes the manager's current associate build to the host. Hosts otherwise
  // keep the binary they were enrolled with, so module fixes shipped with the manager never
  // reach them.
  async submitUpgrade() {
    const body = { sshUser: this.upgrade.sshUser, sshPassword: this.upgrade.sshPassword };
    const r = await fetch(`/api/v1/hosts/${this.upgrade.hostId}/upgrade`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    this.upgrade.open = false;
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      alert('upgrade failed: ' + ((e.error && e.error.message) || r.status));
      return;
    }
    const { jobId } = await r.json();
    const hostName = this.upgrade.hostName;
    await this.refresh();
    if (jobId) this.watchJob(jobId, 'upgrade associate ' + hostName);
  },
  openUpgradeAll() {
    this.upgradeAll = { open: true, busy: false, error: '' };
  },
  // jobFailure reads back why an upgrade job failed: the manager's authRequired verdict (put on
  // the job result, so this doesn't sniff error text that either side might reword) and the
  // error itself, which the credential prompt shows so the operator can see what the host said.
  //
  // Fetched per failed host rather than carried on the watchJob promise, which resolves with a
  // state string only — one extra request, and only for hosts that actually failed.
  async jobFailure(jobId) {
    try {
      const r = await fetch(`/api/v1/jobs/${jobId}`);
      if (!r.ok) return { authRequired: false, error: '' };
      const j = await r.json();
      const res = typeof j.result === 'string' ? JSON.parse(j.result) : j.result;
      return { authRequired: !!(res && res.authRequired), error: j.error || '' };
    } catch (e) { return { authRequired: false, error: '' }; }
  },
  // submitUpgradeAll pushes the manager's associate build to every host the manager considers
  // stale. The manager picks the targets, not this code: the button would otherwise upgrade a
  // set that disagreed with the count next to it whenever the two staleness rules drifted.
  //
  // No credentials go with the first pass. Hosts differ, so anything typed up front would be
  // wrong for most of them; instead every host is tried with the manager's key/agent and its
  // own stored user, and only the ones that come back needing a login get asked about.
  //
  // Each host gets its own job in the docked panel, so the fleet upgrade stays watchable host
  // by host and one failure never hides the rest.
  async submitUpgradeAll() {
    this.upgradeAll.busy = true;
    this.upgradeAll.error = '';
    let started;
    try {
      const r = await fetch('/api/v1/hosts/upgrade-stale', { method: 'POST' });
      if (!r.ok) {
        const e = await r.json().catch(() => ({}));
        this.upgradeAll.error = 'bulk upgrade failed: ' + ((e.error && e.error.message) || r.status);
        return;
      }
      const out = await r.json().catch(() => ({}));
      started = out.started || [];
      const failed = out.failed || [];
      this.upgradeAll.open = false;
      await this.refresh();
      // Hosts the manager could not even start on (no SSH path, host vanished). No password
      // fixes these, so they are reported once and not queued for a prompt.
      if (failed.length) {
        alert('Could not start an upgrade on:\n' + failed.map(f => `${f.hostName}: ${f.error}`).join('\n'));
      }
      if (!started.length) {
        if (!failed.length) alert('No host is running older associate code — nothing to upgrade.');
        return;
      }
    } finally {
      this.upgradeAll.busy = false;
    }

    // Watch every job concurrently — they run in parallel on the manager, so awaiting them in
    // sequence would report the first host's outcome long after the last host had finished.
    const needCreds = [];
    await Promise.all(started.map(async s => {
      const state = await this.watchJob(s.jobId, 'upgrade associate ' + s.hostName,
        { hostId: s.hostId, module: 'quartermaster', action: 'upgrade' });
      if (state !== 'failed') return;
      // Every failure is offered a retry, not only the ones the manager could name as a
      // credentials problem. Recognising a rejected login means matching SSH and sudo error
      // text, and a phrase that list has not learned yet used to end the host's turn silently —
      // the operator saw "failed" and had no way back in. authRequired now only decides how the
      // prompt is worded; skipping a host it cannot help is one click.
      const { authRequired, error } = await this.jobFailure(s.jobId);
      needCreds.push({ hostId: s.hostId, hostName: s.hostName, authRequired, error });
    }));
    await this.refresh();
    this.queueCredPrompts(needCreds);
  },
  // queueCredPrompts starts asking for credentials, one host at a time. A modal per host at
  // once would be unusable on a fleet; a queue lets the operator work through them, and skip
  // any host they do not have a login for without abandoning the rest.
  queueCredPrompts(hosts) {
    if (!hosts.length) return;
    // Hosts the manager recognised as needing a login come first, then the rest by name. Both
    // are offered a retry (see submitUpgradeAll), but the ones a password is known to fix are
    // worth the operator's attention before the ones it probably will not.
    //
    // Merged into whatever is already queued, not assigned over it: backgrounded retries land
    // here as they fail (see trackCredRetry), and replacing the queue would drop the hosts still
    // waiting — or, with two landing at once, drop the prompt already on screen.
    this.credQueue = [...this.credQueue, ...hosts].sort((a, b) => {
      if (!!b.authRequired !== !!a.authRequired) return b.authRequired ? 1 : -1;
      return (a.hostName || '').localeCompare(b.hostName || '');
    });
    if (this.credPrompt.open) { this.credPrompt.remaining = this.credQueue.length; return; }
    this.nextCredPrompt();
  },
  nextCredPrompt() {
    const next = this.credQueue.shift();
    if (!next) { this.credPrompt.open = false; return; }
    const h = this.hosts.find(x => x.id === next.hostId) || {};
    this.credPrompt = {
      open: true, hostId: next.hostId, hostName: next.hostName,
      // Prefilled with the host's stored user: the password is usually the only missing part,
      // and retyping a username the manager already knows is pure friction. origUser is kept so
      // a correction can be written back to the host record on success (see submitCredPrompt).
      sshUser: h.sshUser || '', origUser: h.sshUser || '', sshPassword: '',
      authRequired: !!next.authRequired, jobError: next.error || '',
      error: '', busy: false, remaining: this.credQueue.length,
    };
  },
  skipCredPrompt() { this.nextCredPrompt(); },
  // skipAllCredPrompts abandons the whole queue. Since every failed host is offered a retry, a
  // fleet-wide problem (manager rebooted, network down) would otherwise mean clicking Skip once
  // per host to get out.
  skipAllCredPrompts() { this.credQueue = []; this.credPrompt.open = false; },
  // submitCredPrompt retries one host with the credentials just given, and only waits long
  // enough to find out whether the login was accepted.
  //
  // A rejected login fails inside credGraceMs and is answered in place: the same host stays on
  // screen with the error and its queue position, because a typo must not cost it its turn. A
  // retry still running after that is past the credentials and into the upload and restart —
  // tens of seconds of work with nothing to decide — so it goes to the docked job panel and the
  // next host is asked about immediately. Its outcome is still collected (see trackCredRetry).
  async submitCredPrompt() {
    const host = {
      hostId: this.credPrompt.hostId, hostName: this.credPrompt.hostName,
      sshUser: this.credPrompt.sshUser, origUser: this.credPrompt.origUser,
    };
    const sshPassword = this.credPrompt.sshPassword;
    this.credPrompt.busy = true;
    this.credPrompt.error = '';
    try {
      const r = await fetch(`/api/v1/hosts/${host.hostId}/upgrade`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sshUser: host.sshUser, sshPassword }),
      });
      if (!r.ok) {
        const e = await r.json().catch(() => ({}));
        this.credPrompt.error = 'upgrade failed: ' + ((e.error && e.error.message) || r.status);
        return;
      }
      const { jobId } = await r.json().catch(() => ({}));
      if (!jobId) { this.credPrompt.error = 'the manager started no job for this host.'; return; }

      const watch = this.watchJob(jobId, 'upgrade associate ' + host.hostName,
        { hostId: host.hostId, module: 'quartermaster', action: 'upgrade' });
      const grace = new Promise(res => this._later(() => res(null), credGraceMs));
      const early = await Promise.race([watch, grace]);

      if (early === 'failed') { await this.showCredFailure(jobId); return; }
      if (early === null) {
        this.trackCredRetry(watch, jobId, host).catch(() => {});
        this.nextCredPrompt();
        return;
      }
      await this.finishCredRetry(host);
      await this.refresh();
      this.nextCredPrompt();
    } finally {
      this.credPrompt.busy = false;
    }
  },
  // showCredFailure re-opens the current prompt with what the manager said, keeping the host on
  // screen for another attempt.
  async showCredFailure(jobId) {
    const { authRequired, error } = await this.jobFailure(jobId);
    await this.refresh();
    this.credPrompt.jobError = error;
    this.credPrompt.sshPassword = '';
    this.credPrompt.authRequired = authRequired;
    this.credPrompt.error = authRequired
      ? 'Those credentials were rejected. Try again, or skip this host.'
      : 'That login got in, but the upgrade still failed — see the error below and this host\'s job log.';
  },
  // trackCredRetry follows a backgrounded retry to its end. It must not touch credPrompt: by the
  // time this resolves the operator is several hosts further on, and writing into the live
  // prompt would put one host's error over another host's form.
  //
  // A late failure is re-queued rather than dropped. Not every credentials problem is visible at
  // login — a sudo password the host will not take only surfaces after the binaries have
  // uploaded — so the host goes back in the queue with the error the manager recorded.
  async trackCredRetry(watch, jobId, host) {
    const state = await watch;
    await this.refresh();
    if (state !== 'failed') { await this.finishCredRetry(host); return; }
    const { authRequired, error } = await this.jobFailure(jobId);
    const entry = { hostId: host.hostId, hostName: host.hostName, authRequired, error };
    if (this.credPrompt.open || this.credQueue.length) {
      this.credQueue.push(entry);
      this.credPrompt.remaining = this.credQueue.length; // the count on screen just changed
      return;
    }
    // The queue already ran dry, so nothing will pick this up: ask about it now.
    this.queueCredPrompts([entry]);
  },
  // finishCredRetry persists a corrected username after a retry that worked. The stored user is
  // what the next bulk upgrade tries first, so leaving it wrong means this host fails and asks
  // again every single time.
  async finishCredRetry(host) {
    if (host.sshUser && host.sshUser !== host.origUser) {
      await this.saveHostSSHUser(host.hostId, host.sshUser);
    }
  },
  // saveHostSSHUser persists a corrected SSH username on the host record. Best-effort: the
  // upgrade already succeeded, so a failure here costs one retyped username next time, not the
  // work just done.
  async saveHostSSHUser(hostId, sshUser) {
    try {
      await fetch(`/api/v1/hosts/${hostId}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sshUser }),
      });
    } catch (e) { /* the upgrade landed; the username is a convenience */ }
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
        for (const st of s.stacks) for (const sv of (st.services || [])) if (this.svcUpdate(sv)) n++;
      }
    }
    return n;
  },
  capabilities(m) {
    const c = m.detection && m.detection.capabilities || {};
    return Object.entries(c).map(([k, v]) => `${k}=${v}`).join(' ');
  },
  openEditHost(h) {
    this.editHostId = h.id;
    this.editHost = {
      name: h.name || '', ip: h.ip || '', sshUser: h.sshUser || '', tailscale: !!h.tailscale,
      connMode: h.connMode || 'manager_dial', connPort: h.connPort || null,
    };
    this.editHostOrig = { connMode: this.editHost.connMode, connPort: this.editHost.connPort };
    this.editHostOpen = true;
  },
  // True when the edit touches a field baked into the associate at install time (stream
  // direction or listen port), so the change only takes effect after a reinstall.
  hostNeedsReinstall() {
    const port = Number(this.editHost.connPort) || 0;
    const origPort = Number(this.editHostOrig.connPort) || 0;
    return this.editHost.connMode !== this.editHostOrig.connMode || port !== origPort;
  },
  async submitEditHost() {
    const body = {
      name: this.editHost.name, ip: this.editHost.ip, sshUser: this.editHost.sshUser,
      tailscale: this.editHost.tailscale, connMode: this.editHost.connMode,
      connPort: Number(this.editHost.connPort) || 0,
    };
    const r = await fetch(`/api/v1/hosts/${this.editHostId}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    if (!r.ok) { const e = await r.json().catch(() => ({})); alert('save failed: ' + (e.error?.message || r.status)); return; }
    this.editHostOpen = false;
    this.refresh();
  },
  async submitHost() {
    const body = { ...this.newHost, connPort: Number(this.newHost.connPort) || 0 };
    const r = await fetch('/api/v1/hosts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) { alert('enroll failed'); return; }
    const { jobId } = await r.json().catch(() => ({}));
    const hostName = this.newHost.name;
    this.addHostOpen = false;
    this.newHost = { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false, connMode: 'manager_dial', connPort: null };
    this.page = 'hosts';
    await this.refresh();
    if (jobId) this.watchJob(jobId, 'enroll ' + hostName);
  },
};
