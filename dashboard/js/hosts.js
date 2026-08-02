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
  // associateStale reports that a host runs a different associate build than the one the
  // manager would deploy — i.e. it is missing whatever shipped with the manager since. Both
  // sides must be known: an offline host reports no build, and a manager whose associate
  // binary carries no VCS stamp has nothing to compare, and neither is evidence of staleness.
  associateStale(h) {
    const target = this.overview && this.overview.associateBuild;
    return !!target && !!h.associateVersion && h.associateVersion !== target;
  },
  // staleAssociates counts hosts needing an upgrade, for the banner on every page.
  staleAssociates() { return this.hosts.filter(h => this.associateStale(h)).length; },
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
