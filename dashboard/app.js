// Alpine component backing the LabAssistant dashboard (index.html). Loaded as a plain
// (non-deferred) script before the deferred Alpine bundle, so app() is defined by the time
// Alpine initializes x-data="app()".
function app() {
  return {
    page: 'overview',
    overview: {},
    hosts: [],
    services: { stacks: [] },
    tasks: [],
    approvals: [],
    sudoPrompts: [],
    sudoModal: { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' },
    audit: [],
    expanded: null,
    addHostOpen: false,
    taskOpen: false,
    newHost: { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false },
    newTask: { name: '', schedule: '', module: '', action: '', hostIds: [], misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false },
    job: { open: false, state: '', progress: 0, log: [] },
    logView: { open: false, title: '', lines: [], es: null },
    ready: false,
    needsLogin: false,
    authUser: '',
    login: { username: '', password: '' },
    loginError: '',
    settings: { logLevel: 'info', defaultTimezone: '' },
    tokens: [],
    newTokenName: '',
    newTokenAudit: false,
    newTokenValue: '',
    auditError: '',
    cfg: { open: false, hostId: '', module: '', fields: [], values: {} },
    act: { open: false, hostId: '', module: '', action: '', destructive: false, fields: [], values: {}, error: '' },
    uninstall: { open: false, hostId: '', hostName: '', online: false, sshUser: '', sshPassword: '' },

    async init() {
      try {
        const r = await fetch('/api/v1/auth/session');
        if (!r.ok) { this.needsLogin = true; return; }
        const s = await r.json();
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
    },
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
        this.tasks = await (await fetch('/api/v1/tasks')).json();
        this.approvals = await (await fetch('/api/v1/approvals')).json();
        this.sudoPrompts = await (await fetch('/api/v1/sudo')).json();
        const ar = await fetch('/api/v1/audit');
        if (ar.ok) { this.audit = await ar.json(); this.auditError = ''; }
        else { this.audit = []; this.auditError = ar.status === 403 ? 'Audit access not permitted for this credential.' : 'Failed to load audit log.'; }
        this.settings = await (await fetch('/api/v1/settings')).json();
        this.tokens = await (await fetch('/api/v1/auth/tokens')).json();
      } catch (e) { console.error(e); }
    },
    async saveSettings() {
      await fetch('/api/v1/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(this.settings) });
      this.refresh();
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
    openTask() {
      this.newTask = { name: '', schedule: '', module: '', action: '', hostIds: [], misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false };
      this.taskOpen = true;
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
      if (r.ok) { const { jobId } = await r.json(); this.refresh(); if (jobId) this.watchJob(jobId); }
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
      const { jobId } = await r.json().catch(() => ({}));
      this.sudoModal = { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' };
      this.refresh();
      if (jobId) this.watchJob(jobId);
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
      await this.refresh();
      if (jobId) this.watchJob(jobId);
    },
    statusClass(s) {
      return {
        online: 'is-success', offline: 'is-danger', enrolling: 'is-warning',
        error: 'is-danger', succeeded: 'is-success', failed: 'is-danger',
        running: 'is-info', partial: 'is-warning', pending: 'is-warning',
        stopped: 'is-danger'
      }[s] || 'is-light';
    },
    capabilities(m) {
      const c = m.detection && m.detection.capabilities || {};
      return Object.entries(c).map(([k, v]) => `${k}=${v}`).join(' ');
    },
    watchJob(jobId) {
      this.job = { open: true, state: 'running', progress: 0, log: [] };
      const es = new EventSource(`/api/v1/jobs/${jobId}/events`);
      es.onmessage = (e) => {
        const ev = JSON.parse(e.data).payload;
        if (ev.kind === 'log') this.job.log.push(ev.message);
        if (ev.kind === 'progress') this.job.progress = ev.progress;
        if (ev.kind === 'state') {
          this.job.state = ev.state;
          if (['succeeded', 'failed', 'timed_out', 'needs_sudo_password'].includes(ev.state)) { es.close(); this.refresh(); }
        }
      };
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
      if (!r.ok) { this.job = { open: true, state: 'failed', progress: 0, log: ['dispatch failed'] }; return; }
      const out = await r.json();
      if (out.approvalId) {
        this.job = {
          open: true, state: 'pending', progress: 0,
          log: ['This action requires approval. Confirm it in the “Pending approvals” banner at the top of the page.']
        };
        await this.refresh();
        return;
      }
      if (out.jobId) this.watchJob(out.jobId);
    },
    svcAction(stack, service, action) {
      const params = service ? { stack: stack.name, service } : { stack: stack.name };
      this.runAction(stack.hostId, 'duo', action, params);
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
      const r = await fetch('/api/v1/hosts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(this.newHost),
      });
      if (!r.ok) { alert('enroll failed'); return; }
      const { jobId } = await r.json();
      this.addHostOpen = false;
      this.newHost = { name: '', ip: '', sshUser: '', sshPassword: '', tailscale: false };
      this.page = 'hosts';
      await this.refresh();
      this.watchJob(jobId);
    },
  };
}
