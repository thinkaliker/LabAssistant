// Hosts page: list ordering, per-host expansion, module config, add/edit/uninstall/revive flows.
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
  credQueue: [], // [{hostId, hostName}] awaiting credentials, oldest first
  credPrompt: { open: false, hostId: '', hostName: '', sshUser: '', sshPassword: '', error: '', busy: false, remaining: 0 },

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
  // jobAuthRequired reports whether a finished upgrade job failed for want of credentials, as
  // opposed to failing for a reason no password will fix. The manager decides this and puts
  // authRequired on the job result; reading it back beats sniffing the error text here, which
  // would drift the moment either side reworded anything.
  //
  // Fetched per failed host rather than carried on the watchJob promise, which resolves with a
  // state string only — one extra request, and only for hosts that actually failed.
  async jobAuthRequired(jobId) {
    try {
      const r = await fetch(`/api/v1/jobs/${jobId}`);
      if (!r.ok) return false;
      const j = await r.json();
      const res = typeof j.result === 'string' ? JSON.parse(j.result) : j.result;
      return !!(res && res.authRequired);
    } catch (e) { return false; }
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
      if (state === 'failed' && await this.jobAuthRequired(s.jobId)) {
        needCreds.push({ hostId: s.hostId, hostName: s.hostName });
      }
    }));
    await this.refresh();
    this.queueCredPrompts(needCreds);
  },
  // queueCredPrompts starts asking for credentials, one host at a time. A modal per host at
  // once would be unusable on a fleet; a queue lets the operator work through them, and skip
  // any host they do not have a login for without abandoning the rest.
  queueCredPrompts(hosts) {
    if (!hosts.length) return;
    // Stable order, so the queue reads the same way as the hosts list.
    this.credQueue = [...hosts].sort((a, b) => (a.hostName || '').localeCompare(b.hostName || ''));
    this.nextCredPrompt();
  },
  nextCredPrompt() {
    const next = this.credQueue.shift();
    if (!next) { this.credPrompt.open = false; return; }
    const h = this.hosts.find(x => x.id === next.hostId) || {};
    this.credPrompt = {
      open: true, hostId: next.hostId, hostName: next.hostName,
      // Prefilled with the host's stored user: the password is usually the only missing part,
      // and retyping a username the manager already knows is pure friction.
      sshUser: h.sshUser || '', sshPassword: '',
      error: '', busy: false, remaining: this.credQueue.length,
    };
  },
  skipCredPrompt() { this.nextCredPrompt(); },
  // submitCredPrompt retries one host with the credentials just given. A second credentials
  // failure re-opens the same host with the error shown rather than advancing the queue —
  // a typo must not cost the host its turn.
  async submitCredPrompt() {
    const { hostId, hostName, sshUser, sshPassword } = this.credPrompt;
    this.credPrompt.busy = true;
    this.credPrompt.error = '';
    try {
      const r = await fetch(`/api/v1/hosts/${hostId}/upgrade`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sshUser, sshPassword }),
      });
      if (!r.ok) {
        const e = await r.json().catch(() => ({}));
        this.credPrompt.error = 'upgrade failed: ' + ((e.error && e.error.message) || r.status);
        return;
      }
      const { jobId } = await r.json().catch(() => ({}));
      if (!jobId) { this.credPrompt.error = 'the manager started no job for this host.'; return; }
      const state = await this.watchJob(jobId, 'upgrade associate ' + hostName,
        { hostId, module: 'quartermaster', action: 'upgrade' });
      await this.refresh();
      if (state === 'failed' && await this.jobAuthRequired(jobId)) {
        this.credPrompt.error = 'Those credentials were rejected. Try again, or skip this host.';
        this.credPrompt.sshPassword = '';
        return;
      }
      if (state === 'failed') {
        // Not a credentials problem this time — nothing typed here will fix it, so move on
        // and leave the job's log in the panel as the record of what went wrong.
        alert(`${hostName}: upgrade failed for a reason credentials will not fix — see its job log.`);
      }
      this.nextCredPrompt();
    } finally {
      this.credPrompt.busy = false;
    }
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
