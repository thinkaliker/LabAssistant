// .env editor: a dialog for the stack's project .env (the file docker compose reads for ${VAR}
// substitution), opened from the compose panel. Rows edit the raw text through dotenv.js; the Raw
// view edits it directly.

import { parseEnv, envRows, envOps } from './dotenv.js';

// Mirrors maxEnvBytes in modules/duo/env.go.
const MAX_ENV_BYTES = 256 * 1024;

const closedEnv = () => ({
  open: false, loading: false, busy: false, hostId: '', stack: '', path: '', target: '', outsideDir: false,
  exists: false, truncated: false, sha256: '', mode: 'simple', raw: '', original: '', rows: [], reveal: [],
  error: '', status: '',
});

export const envedit = {
  envEdit: closedEnv(),

  // openEnv reads the .env for the stack open in the compose panel.
  async openEnv() {
    const { hostId, stack } = this.compose;
    if (!hostId || !stack || this.envEdit.loading) return;
    this.envEdit = { ...closedEnv(), loading: true, hostId, stack };
    try {
      const out = await this.dispatchSilent(hostId, 'duo', 'read-env', { stack });
      if (!out || !out.jobId) { this.envEdit.loading = false; this.compose.error = 'Could not start reading the .env file.'; return; }
      const res = await this.awaitJob(out.jobId);
      if (res.job && res.job.state === 'needs_sudo_password') {
        this.envEdit.loading = false;
        this.compose.error = 'Sudo password required — provide it in the banner above.';
        this.refresh();
        return;
      }
      this.openEnvFromJob(res);
    } catch (e) {
      console.error(e);
      this.envEdit.loading = false;
      this.compose.error = 'Error reading the .env file.';
    }
  },
  // Takes an awaitJob result (also used when a sudo prompt re-dispatches the read).
  openEnvFromJob(outcome) {
    const job = outcome && outcome.job;
    this.envEdit.loading = false;
    if (!job || job.state !== 'succeeded' || !job.result) {
      this.compose.error = outcome && outcome.timedOut
        ? 'Still reading the .env file — check the job panel, then try again.'
        : 'Failed to read the .env file: ' + ((job && job.error) || 'unknown error');
      return;
    }
    const res = typeof job.result === 'string' ? JSON.parse(job.result) : job.result;
    const raw = String(res.content || '').replace(/\r\n?/g, '\n');
    this.envEdit = {
      ...closedEnv(), open: true, hostId: job.hostId, stack: res.stack || this.compose.stack, path: res.path || '',
      target: res.target || '', outsideDir: !!res.outsideDir, exists: !!res.exists, truncated: !!res.truncated,
      sha256: res.sha256 || '', raw, original: raw,
    };
    this.envRebuild();
  },
  envRebuild() {
    this.envEdit.rows = envRows(parseEnv(this.envEdit.raw));
  },
  envDirty() {
    return this.envEdit.open && this.envEdit.raw !== this.envEdit.original;
  },
  envEditOp(fn) {
    try {
      this.envEdit.raw = fn(parseEnv(this.envEdit.raw));
      this.envEdit.error = '';
      this.envEdit.status = '';
      this.envRebuild();
    } catch (e) {
      this.envEdit.error = e.message;
      this.envRebuild(); // put inputs back to what the file says
    }
  },
  envSet(row, value) {
    if (value !== row.value) this.envEditOp((m) => envOps.setValue(m, row.line, value));
  },
  envSetKey(row, key) {
    if (key.trim() !== row.key) this.envEditOp((m) => envOps.setKey(m, row.line, key));
  },
  envToggle(row) {
    this.envEditOp((m) => envOps.toggle(m, row.line));
  },
  envRemove(row) {
    this.envEditOp((m) => envOps.remove(m, row.line));
  },
  envAddFrom(el) {
    const box = el.closest('.env-add');
    const keyEl = box.querySelector('.env-add-key');
    const valueEl = box.querySelector('.env-add-value');
    const key = keyEl.value.trim();
    if (!key) { this.envEdit.error = 'Enter a variable name.'; return; }
    const before = this.envEdit.raw;
    this.envEditOp((m) => envOps.add(m, key, valueEl.value));
    if (this.envEdit.raw !== before) { keyEl.value = ''; valueEl.value = ''; keyEl.focus(); }
  },
  envRevealed(row) {
    return this.envEdit.reveal.includes(row.id);
  },
  envToggleReveal(row) {
    const r = this.envEdit.reveal;
    this.envEdit.reveal = r.includes(row.id) ? r.filter((x) => x !== row.id) : [...r, row.id];
  },
  envSetMode(mode) {
    if (mode === this.envEdit.mode) return;
    if (mode === 'simple') this.envRebuild();
    this.envEdit.mode = mode;
  },

  async saveEnv(redeploy) {
    const e = this.envEdit;
    if (e.busy || e.truncated) return;
    e.error = ''; e.status = '';
    const content = e.raw;
    if (new Blob([content]).size > MAX_ENV_BYTES) { e.error = 'The .env file is larger than 256 KiB.'; return; }
    e.busy = true;
    try {
      const out = await this.dispatchSilent(e.hostId, 'duo', 'write-env', { stack: e.stack, content, baseSha256: e.sha256 });
      if (!out || !out.jobId) { e.error = 'Could not start save.'; return; }
      const res = await this.awaitJob(out.jobId);
      const job = res.job;
      if (job && job.state === 'needs_sudo_password') { e.error = 'Sudo password required — provide it in the banner, then save again.'; this.refresh(); return; }
      // As with the compose file: slow is not failed, and saying so invites a second write.
      if (res.timedOut) { e.error = 'Save is still running — watch the job panel, and reopen the file before saving again.'; return; }
      if (!job || job.state !== 'succeeded') { e.error = (job && job.error) || 'Save failed.'; return; }
      let result = job.result;
      try { if (typeof result === 'string') result = JSON.parse(result); } catch (err) { result = null; }
      e.sha256 = (result && result.sha256) || '';
      e.original = content;
      e.exists = true;
      e.status = redeploy ? 'Saved. Redeploy queued — confirm it in the approvals banner.' : 'Saved.';
    } finally {
      e.busy = false;
    }
    if (redeploy && !e.error) await this.runAction(e.hostId, 'duo', 'deploy', { stack: e.stack });
  },
  // envCancel vetoes Esc / backdrop closes while saving, or when there are unsaved edits the user
  // wants to keep.
  envCancel(ev) {
    if (this.envEdit.busy || (this.envDirty() && !confirm('Discard unsaved changes to the .env file?'))) ev.preventDefault();
  },
  closeEnv() {
    if (this.envEdit.busy) return;
    if (this.envDirty() && !confirm('Discard unsaved changes to the .env file?')) return;
    this.envEdit = closedEnv();
  },
  // envClosed handles the dialog closing itself (Esc or backdrop, already confirmed by envCancel).
  envClosed() {
    this.envEdit = closedEnv();
  },
};
