// Simple compose editor: the form view of the compose panel. The YAML text is the source of truth
// (see composedoc.js); this module turns row events into operations on it and re-renders.
//
// The engine, schema helpers and the YAML library are imported on first use, so they cost nothing
// until someone opens the editor.

// Parsed state lives outside Alpine's reactive proxy, like the CodeMirror instance: rows are the
// only thing the template needs, and proxying the YAML AST would be slow for nothing.
let lib = null; // { load, apply, normalize, lint, buildRows, pickerFields }
let schema = null; // createSchema(...) result
let doc = null; // current Doc
let undo = []; // previous texts, newest last
const ui = { toggled: new Set(), shown: new Set() };
const pending = new Map(); // row id -> { row, value } typed but not yet committed

const emptyPicker = () => ({ open: false, entryId: '', title: '', query: '', fields: [], custom: '', allowCustom: false });

export const composeform = {
  cform: { loading: false, blocked: '', rows: [], error: '', warnings: [], canUndo: false, picker: emptyPicker() },

  async cfLib() {
    if (!lib) {
      const [d, s, r, spec] = await Promise.all([
        import('./composedoc.js'), import('./composeschema.js'), import('./composerows.js'),
        fetch('/vendor/compose-spec.json').then((res) => res.json()),
      ]);
      schema = s.createSchema(spec);
      lib = { ...d, buildRows: r.buildRows, pickerFields: r.pickerFields };
    }
    return lib;
  },

  // cfLoad parses text into the form. It returns false (with cform.blocked set) when the file
  // can't be shown as a form, or the editor code couldn't be loaded.
  async cfLoad(text) {
    this.cform.loading = true;
    try {
      await this.cfLib();
    } catch (e) {
      console.error(e);
      this.cform.loading = false;
      this.cform.blocked = 'The simple editor could not be loaded.';
      return false;
    }
    this.cform.loading = false;
    const next = lib.load(lib.normalize(text).text, schema.hooks);
    if (next.blocked) {
      this.cform.blocked = next.blocked;
      return false;
    }
    if (!doc || doc.text !== next.text) {
      undo = [];
      pending.clear();
    }
    doc = next;
    this.cform.blocked = '';
    this.cform.error = '';
    this.cfRebuild();
    return true;
  },
  cfReset() {
    doc = null;
    undo = [];
    pending.clear();
    ui.toggled.clear();
    ui.shown.clear();
    this.cform = { loading: false, blocked: '', rows: [], error: '', warnings: [], canUndo: false, picker: emptyPicker() };
  },
  // cfText is the form's current text, or null when no document is loaded.
  cfText() {
    return doc ? doc.text : null;
  },
  cfRebuild() {
    if (!doc) return;
    this.cform.rows = lib.buildRows(doc, schema, ui);
    this.cform.warnings = lib.lint(doc.js);
    this.cform.canUndo = undo.length > 0;
  },
  cfApply(ops) {
    if (!doc) return false;
    const r = lib.apply(doc, ops, schema.hooks);
    if (!r.ok) {
      this.cform.error = r.error;
      if (r.bug) console.error(r.bug);
      return false;
    }
    if (r.text !== doc.text) undo.push(doc.text);
    doc = r.doc;
    this.cform.error = '';
    this.cfRebuild();
    return true;
  },
  cfUndo() {
    if (!undo.length) return;
    doc = lib.load(undo.pop(), schema.hooks);
    pending.clear();
    this.cform.error = '';
    this.cfRebuild();
  },

  // ---- row events ----
  cfPending(row, value) {
    pending.set(row.id, { row, value });
  },
  // cfFlush commits values typed into a field whose change event hasn't fired yet (a tap on Save
  // on iOS can land before the input blurs).
  cfFlush() {
    for (const { row, value } of [...pending.values()]) this.cfSet(row, value);
    pending.clear();
  },
  cfSet(row, value) {
    pending.delete(row.id);
    if (value === row.value) return;
    if (row.t === 'flowitem') {
      const e = doc && doc.byId.get(row.entryId);
      if (!e || !e.value.items) return;
      const items = e.value.items.map((it, i) => (i === row.index ? { key: it.key, keySrc: it.keySrc, value } : it));
      this.cfApply({ op: 'flowItems', id: row.entryId, items });
      return;
    }
    this.cfApply({ op: 'set', id: row.entryId, value });
  },
  cfRename(row, key) {
    key = String(key || '').trim();
    if (key === row.label) return;
    if (row.t === 'flowitem') {
      const e = doc && doc.byId.get(row.entryId);
      if (!e || !e.value.items) return;
      const items = e.value.items.map((it, i) => (i === row.index ? { key, value: it.value, src: it.src } : it));
      this.cfApply({ op: 'flowItems', id: row.entryId, items });
      return;
    }
    if (!this.cfApply({ op: 'rename', id: row.entryId, key })) this.cfRebuild(); // restore the input
  },
  cfRenamePrompt(row) {
    const key = prompt(`Rename "${row.label}" to:`, row.label);
    if (key != null) this.cfRename(row, key);
  },
  cfToggle(row) {
    if (row.t === 'disabled') this.cfApply({ op: 'enable', id: row.entryId });
    else this.cfApply({ op: 'disable', id: row.toggleTarget || row.entryId });
  },
  cfRemove(row) {
    if (row.t === 'flowitem') {
      const e = doc && doc.byId.get(row.entryId);
      if (!e || !e.value.items) return;
      this.cfApply({ op: 'flowItems', id: row.entryId, items: e.value.items.filter((_, i) => i !== row.index) });
      return;
    }
    this.cfApply({ op: 'remove', id: row.entryId });
  },
  cfToBlock(row) {
    this.cfApply({ op: 'toBlock', id: row.entryId });
  },
  cfToggleOpen(row) {
    if (ui.toggled.has(row.entryId)) ui.toggled.delete(row.entryId);
    else ui.toggled.add(row.entryId);
    this.cfRebuild();
  },
  cfToggleFragment(row) {
    if (ui.shown.has(row.entryId)) ui.shown.delete(row.entryId);
    else ui.shown.add(row.entryId);
    this.cfRebuild();
  },
  // cfCollapseAll closes every open group, outermost included, except the top-level sections.
  cfCollapseAll() {
    for (let pass = 0; pass < 12; pass++) {
      const open = this.cform.rows.filter((r) => r.t === 'group' && r.open);
      if (!open.length) break;
      for (const r of open) {
        if (ui.toggled.has(r.entryId)) ui.toggled.delete(r.entryId);
        else ui.toggled.add(r.entryId);
      }
      this.cfRebuild();
    }
  },
  // ensureOpen makes sure a group shows its children (after something was added to it).
  cfEnsureOpen(entryId) {
    const row = this.cform.rows.find((r) => r.entryId === entryId && (r.t === 'group' || r.t === 'section'));
    if (row && !row.open) this.cfToggleOpen(row);
  },

  // cfAddFrom reads an add row's inputs (kept in the DOM rather than in state, one set per row).
  cfAddFrom(row, el) {
    const box = el.closest('.cf-row');
    const keyEl = box && box.querySelector('.cf-add-key');
    const valueEl = box && box.querySelector('.cf-add-value');
    const key = keyEl ? keyEl.value.trim() : '';
    const value = valueEl ? valueEl.value : '';
    const e = doc && doc.byId.get(row.entryId);
    if (!e) return;
    let ok = false;
    switch (row.addKind) {
      case 'item':
        ok = this.cfApply({ op: 'add', parent: row.entryId, value });
        break;
      case 'objitem':
        ok = this.cfApply({ op: 'add', parent: row.entryId, kind: 'map' });
        break;
      case 'kv':
        if (!key) { this.cform.error = 'Enter a name.'; return; }
        ok = this.cfApply({ op: 'add', parent: row.entryId, key, value });
        break;
      case 'namedkv':
        if (!key) { this.cform.error = 'Enter a name.'; return; }
        ok = this.cfApply({ op: 'add', parent: row.entryId, key, kind: 'map' });
        break;
      case 'named': {
        if (!key) { this.cform.error = 'Enter a name.'; return; }
        const ops = [{ op: 'add', parent: row.entryId, key, kind: 'map' }];
        // A service needs an image (or a build) to be valid; start it with an empty image field.
        if (row.section === 'services') ops.push({ op: 'add', parent: 'e:' + JSON.stringify([...e.path, key]), key: 'image', value: '' });
        ok = this.cfApply(ops);
        if (ok) this.cfEnsureOpen('e:' + JSON.stringify([...e.path, key]));
        break;
      }
      case 'flowitem':
      case 'flowkv': {
        if (row.addKind === 'flowkv' && !key) { this.cform.error = 'Enter a name.'; return; }
        const items = [...(e.value.items || []), row.addKind === 'flowkv' ? { key, value } : { value }];
        ok = this.cfApply({ op: 'flowItems', id: row.entryId, items });
        break;
      }
      case 'field':
        this.cfOpenPicker(row);
        return;
    }
    if (ok) {
      if (keyEl) keyEl.value = '';
      if (valueEl) valueEl.value = '';
      this.cfEnsureOpen(row.entryId);
    }
  },

  // ---- field picker ----
  cfOpenPicker(row) {
    const e = doc && doc.byId.get(row.entryId);
    if (!e) return;
    const obj = schema.branch(schema.at(doc, e), 'map');
    this.cform.picker = {
      open: true, entryId: row.entryId, title: e.type === 'root' ? 'the file' : (e.key != null ? e.key : 'this item'),
      query: '', fields: lib.pickerFields(doc, schema, row.entryId), custom: '',
      allowCustom: !!(obj && obj.patternProperties && obj.patternProperties['^x-']),
    };
    this.$nextTick(() => this.$refs.cfPickerSearch && this.$refs.cfPickerSearch.focus());
  },
  cfPickerList() {
    const q = this.cform.picker.query.trim().toLowerCase();
    const list = this.cform.picker.fields;
    if (!q) return list;
    return list.filter((f) => f.key.toLowerCase().includes(q) || f.description.toLowerCase().includes(q));
  },
  cfPick(f) {
    const parent = this.cform.picker.entryId;
    const ok = f.disabledId
      ? this.cfApply({ op: 'enable', id: f.disabledId })
      : this.cfApply({ op: 'add', parent, key: f.key, kind: f.addKind, value: '' });
    if (!ok) return;
    this.cform.picker.open = false;
    this.cfEnsureOpen(parent);
  },
  cfPickCustom() {
    const key = this.cform.picker.custom.trim();
    if (!/^x-/.test(key)) { this.cform.error = 'Custom fields must start with x-.'; return; }
    if (this.cfApply({ op: 'add', parent: this.cform.picker.entryId, key, value: '' })) {
      this.cform.picker.open = false;
      this.cfEnsureOpen(this.cform.picker.entryId);
    }
  },
};
