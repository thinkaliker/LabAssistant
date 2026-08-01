// Scheduler page: the task list plus the add/edit task modal (cron editing, module/action/host
// pickers, create/update/delete).
export const scheduler = {
  tasks: [],
  taskOpen: false,
  editingTaskId: null, // null = creating a new task, otherwise the id being edited
  cronFields: false, // Add Task: false = raw cron string, true = 5 separate fields
  cronParts: { m: '*', h: '*', dom: '*', mon: '*', dow: '*' },
  // params has no editor in this modal; it is carried through an edit untouched. Dropping it
  // used to turn a scoped task (duo update of one stack) into a whole-host run that recreated
  // every container on the next fire — and dropping timezone moved the fire time to the
  // manager's own zone.
  newTask: { name: '', schedule: '', timezone: '', module: '', action: '', hostIds: [], params: null, misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false },

  blankTask() {
    return { name: '', schedule: '', timezone: '', module: '', action: '', hostIds: [], params: null, misfire: 'skip', interHostDelaySeconds: 0, enabled: true, allowDestructive: false };
  },
  openTask() {
    this.editingTaskId = null;
    this.newTask = this.blankTask();
    this.cronFields = false;
    this.cronParts = { m: '*', h: '*', dom: '*', mon: '*', dow: '*' };
    this.taskOpen = true;
  },
  // Open the same modal pre-filled from an existing task; submitTask then PUTs instead of POSTs.
  editTask(t) {
    this.editingTaskId = t.id;
    this.newTask = {
      name: t.name || '', schedule: t.schedule || '', timezone: t.timezone || '',
      module: t.module || '', action: t.action || '',
      hostIds: [...(t.hostIds || [])],
      params: t.params ?? null,
      misfire: t.misfire || 'skip',
      interHostDelaySeconds: t.interHostDelaySeconds || 0, enabled: !!t.enabled, allowDestructive: !!t.allowDestructive,
    };
    this.cronFields = false;
    this.cronParts = { m: '*', h: '*', dom: '*', mon: '*', dow: '*' };
    this.taskOpen = true;
  },
  // Toggle between the raw cron string and the 5-field editor, keeping both in sync.
  toggleCronFields() {
    if (!this.cronFields) {
      // switching to fields: parse the current string, filling gaps with '*'
      const p = (this.newTask.schedule || '').trim().split(/\s+/);
      this.cronParts = { m: p[0] || '*', h: p[1] || '*', dom: p[2] || '*', mon: p[3] || '*', dow: p[4] || '*' };
    }
    this.cronFields = !this.cronFields;
  },
  // Recompose the cron string from the 5 fields (called on each field edit).
  syncCron() {
    const c = this.cronParts;
    this.newTask.schedule = [c.m, c.h, c.dom, c.mon, c.dow].map(x => (x || '').trim() || '*').join(' ');
  },
  // Unique module names across all reporting hosts, alphabetical, for the Add Task dropdown.
  taskModuleNames() {
    const set = new Set();
    for (const h of this.hosts) for (const m of (h.modules || [])) set.add(m.name);
    return [...set].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
  },
  // Actions available for the currently-selected module, unioned across hosts, alphabetical.
  taskModuleActions() {
    const set = new Set();
    for (const h of this.hosts) for (const m of (h.modules || [])) {
      if (m.name === this.newTask.module) for (const a of (m.actions || [])) set.add(a.name);
    }
    return [...set].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
  },
  // Hosts sorted by name so the Add Task checkbox list doesn't reshuffle on refresh.
  taskHostsSorted() {
    return [...this.hosts].sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, { sensitivity: 'base' }));
  },
  toggleTaskHost(id) {
    const i = this.newTask.hostIds.indexOf(id);
    if (i >= 0) this.newTask.hostIds.splice(i, 1); else this.newTask.hostIds.push(id);
  },
  async submitTask() {
    const editing = this.editingTaskId;
    const url = editing ? `/api/v1/tasks/${editing}` : '/api/v1/tasks';
    const body = { ...this.newTask };
    // Omit params entirely when there are none rather than sending null: the manager merges a
    // task update over the stored task, so an absent key preserves what is there.
    if (body.params === null || body.params === undefined) delete body.params;
    const r = await fetch(url, { method: editing ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) { const e = await r.json().catch(() => ({})); alert((editing ? 'update' : 'create') + ' failed: ' + (e.error?.message || r.status)); return; }
    this.taskOpen = false;
    this.refresh();
  },
  async removeTask(id) {
    const r = await fetch(`/api/v1/tasks/${id}`, { method: 'DELETE' });
    if (!r.ok) alert('delete failed: ' + r.status);
    this.refresh();
  },
};
