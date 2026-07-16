// Action dispatch: the schema-driven action form, approvals, sudo password hand-off, and the
// generic runAction/svcAction dispatchers.
export const actions = {
  approvals: [],
  sudoPrompts: [],
  sudoModal: { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' },
  act: { open: false, hostId: '', module: '', action: '', destructive: false, fields: [], values: {}, error: '' },

  async confirmApproval(id) {
    // Capture the approval before refresh drops it, so the job chip/panel names the actual
    // module, action, and host instead of a generic "approved action".
    const ap = this.approvals.find(a => a.id === id);
    const r = await fetch(`/api/v1/approvals/${id}/confirm`, { method: 'POST' });
    if (r.ok) {
      const { jobId } = await r.json();
      this.refresh();
      if (jobId) this.watchJob(jobId, ap ? `${ap.module} ${ap.action}` : 'approved action',
        ap ? { hostId: ap.hostId, module: ap.module, action: ap.action } : undefined);
    }
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
    const action = this.sudoModal.action;
    const mod = this.sudoModal.module;
    const { jobId } = await r.json().catch(() => ({}));
    this.sudoModal = { open: false, id: '', hostId: '', module: '', action: '', password: '', error: '' };
    this.refresh();
    if (!jobId) return;
    // A re-dispatched compose read feeds the editor instead of the generic job modal.
    if (action === 'read-compose') { this.openComposeFromJob(await this.awaitJob(jobId)); }
    else this.watchJob(jobId, mod + '/' + action);
  },
  async cancelSudo(id) {
    await fetch(`/api/v1/sudo/${id}/cancel`, { method: 'POST' });
    if (this.sudoModal.id === id) this.sudoModal.open = false;
    this.refresh();
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
    if (!r.ok) {
      const rec = { id: 'err-' + Date.now(), label: mod + '/' + action, state: 'failed', progress: 0, log: ['dispatch failed'] };
      this.showJob(rec); this.jobPanelOpen = true;
      return;
    }
    const out = await r.json();
    if (out.approvalId) {
      // No modal — the queued action surfaces in the "Pending approvals" banner. Refresh so
      // it appears, then scroll it into view so the user sees it without a popup.
      await this.refresh();
      window.scrollTo({ top: 0, behavior: 'smooth' });
      return;
    }
    if (out.jobId) this.watchJob(out.jobId, mod + '/' + action);
  },
  svcAction(stack, service, action) {
    const params = service ? { stack: stack.name, service } : { stack: stack.name };
    this.runAction(stack.hostId, 'duo', action, params);
  },
};
