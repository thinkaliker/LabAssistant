// Flattens a compose Doc (composedoc.js) into the rows the simple editor renders with a single
// x-for — Alpine templates can't recurse, so nesting becomes a depth on each row. Pure: node --test
// imports it.
//
// Row types (row.t):
//   section   top-level key holding a block (services, networks, …)
//   group     a mapping or list: a service, `ports`, a long-syntax volume, …
//   field     key + scalar value            kv    editable key + value (environment, labels)
//   item      scalar list item               flowitem  item of an inline list (`["CMD", "curl"]`)
//   raw       read-only source (anchors, multi-line inline lists)
//   disabled  commented-out entry            add   "+ Add …" row

const NAMED_SECTIONS = new Set(['services', 'networks', 'volumes', 'secrets', 'configs', 'models']);

function getAt(js, path) {
  let v = js;
  for (const p of path) {
    if (v == null) return undefined;
    v = v[p];
  }
  return v;
}

// itemSummary labels a long-syntax list item (`- type: bind …`) by its most telling fields.
function itemSummary(field, v) {
  if (!v || typeof v !== 'object') return '';
  const s = (x) => (x == null ? '' : String(x));
  if (field === 'volumes' && (v.source || v.target)) return `${v.type ? v.type + ' ' : ''}${s(v.source)} → ${s(v.target)}`.trim();
  if (field === 'ports' && (v.target || v.published)) return `${s(v.published) || '(any)'} → ${s(v.target)}${v.protocol ? '/' + v.protocol : ''}`;
  const parts = Object.entries(v).filter(([, x]) => x == null || typeof x !== 'object').slice(0, 2).map(([k, x]) => `${k}: ${s(x)}`);
  return parts.join(', ');
}

export function buildRows(doc, S, ui = {}) {
  const rows = [];
  if (!doc || doc.blocked) return rows;
  const toggled = ui.toggled || new Set();
  const shown = ui.shown || new Set();
  const root = doc.root;
  const liveTop = root.children.filter((c) => !c.disabled);
  const services = doc.js && doc.js.services && typeof doc.js.services === 'object' ? Object.keys(doc.js.services).length : 0;
  const unknown = new Set(S.unknownKeys(doc).map((e) => e.id));

  const fieldKey = (e) => {
    for (let x = e; x && x.type !== 'root'; x = doc.byId.get(x.parent)) if (x.type === 'pair') return x.key;
    return null;
  };

  const defaultOpen = (e, depth, count) => {
    const parent = doc.byId.get(e.parent);
    if (parent && parent.type === 'pair' && parent.key === 'services' && doc.byId.get(parent.parent) === root) return services <= 3;
    if (depth <= 0) return true;
    return depth <= 3 && count <= 12;
  };
  const isOpen = (e, depth, count) => defaultOpen(e, depth, count) !== toggled.has(e.id);

  const toggleTarget = (e) => {
    // Disabling the last field of a long-syntax item would leave `- {}`, which compose rejects for
    // ports and volumes, so the button disables the whole item instead.
    const p = doc.byId.get(e.parent);
    if (e.type === 'pair' && p && p.type === 'item' && p.children.filter((c) => !c.disabled).length === 1) return p.id;
    return e.id;
  };

  const common = (e, depth) => ({
    id: e.id, entryId: e.id, depth, desc: '', warn: unknown.has(e.id) ? 'Not a compose field — compose will reject it' : '',
    canDisable: !(e.parent === 'root' && liveTop.length === 1), canRemove: !(e.parent === 'root' && liveTop.length === 1),
    toggleTarget: toggleTarget(e),
  });

  const emit = (e, depth) => {
    if (e.disabled) {
      const parent = doc.byId.get(e.parent);
      const isService = parent && parent.type === 'pair' && parent.key === 'services' && doc.byId.get(parent.parent) === root;
      let summary = '';
      if (e.valueKind === 'scalar') summary = e.scalar;
      else if (e.valueKind === 'map' || e.valueKind === 'flowmap') {
        summary = e.type === 'item' ? itemSummary(fieldKey(parent), e.js) : e.js && typeof e.js.image === 'string' ? e.js.image : '{…}';
      }
      else if (e.valueKind === 'seq' || e.valueKind === 'flowseq') summary = '[…]';
      else if (e.valueKind === 'block') summary = '|…';
      rows.push({
        id: e.id, t: 'disabled', entryId: e.id, depth, label: e.key != null ? e.key : '', summary,
        fragment: e.fragment.replace(/\n$/, ''), open: shown.has(e.id), duplicate: e.duplicate,
        note: e.duplicate ? `Another "${e.key}" is active here, so this copy can't be enabled.`
          : isService ? 'Save & redeploy removes its container while "Remove orphaned containers" is checked.' : '',
      });
      return;
    }
    const fs = S.at(doc, e);
    const d = fs ? S.describe(fs) : { description: '', enum: null };
    const base = common(e, depth);
    base.desc = d.description;
    const v = e.value;
    const label = e.type === 'pair' ? e.key : '';

    if (e.readonly) {
      rows.push({ ...base, t: 'raw', label, source: doc.text.slice(doc.lineStarts[e.line], e.endLine + 1 < doc.lineStarts.length ? doc.lineStarts[e.endLine + 1] - 1 : doc.text.length).trim(), reason: e.readonly });
      return;
    }

    const parent = doc.byId.get(e.parent);
    const parentSchema = parent ? S.at(doc, parent) : undefined;
    const parentObj = parentSchema !== undefined ? S.branch(parentSchema, 'map') : null;
    const inFreeMap = e.type === 'pair' && parentObj && !(parentObj.properties && Object.prototype.hasOwnProperty.call(parentObj.properties, e.key)) && !!S.freeKeys(parentObj);
    const kinds = fs !== undefined ? S.kinds(fs) : null;
    const containerNull = v.kind === 'null' && kinds && !kinds.has('scalar') && !kinds.has('null') && (kinds.has('map') || kinds.has('seq'));

    if (v.kind === 'scalar' || v.kind === 'block' || (v.kind === 'null' && !containerNull)) {
      const options = S.suggestions(e.key || fieldKey(e), fs) || (S.scalarAllow(fs).boolean && !S.scalarAllow(fs).number ? ['true', 'false'] : null);
      const row = {
        ...base, label, value: v.kind === 'null' ? '' : v.display, multiline: v.kind === 'block' || (v.kind === 'scalar' && !v.singleLine),
        options,
      };
      if (e.type === 'item') rows.push({ ...row, t: 'item' });
      else if (inFreeMap) rows.push({ ...row, t: 'kv', canRename: true });
      else rows.push({ ...row, t: 'field' });
      return;
    }

    if (v.kind === 'flowseq' || v.kind === 'flowmap') {
      if (!v.empty && v.items) {
        const count = v.items.length;
        const open = isOpen(e, depth, count);
        rows.push({ ...base, t: 'group', label: label || '(list)', open, count, flow: true, summary: v.items.map((it) => (v.kind === 'flowmap' ? `${it.key}: ${it.value}` : it.value)).join(', ') });
        if (!open) return;
        v.items.forEach((it, i) => rows.push({
          id: `${e.id}~${i}`, t: 'flowitem', entryId: e.id, index: i, depth: depth + 1, label: v.kind === 'flowmap' ? it.key : '',
          value: it.value, canRemove: true, canDisable: false,
        }));
        rows.push({ id: `${e.id}+`, t: 'add', addKind: v.kind === 'flowmap' ? 'flowkv' : 'flowitem', entryId: e.id, depth: depth + 1, label: v.kind === 'flowmap' ? 'Add entry' : 'Add item' });
        return;
      }
    }

    // Containers: block mappings and lists, empty inline ones (`ports: []`), null list/map fields.
    const kids = e.children;
    const isSeqLike = v.kind === 'seq' || v.kind === 'flowseq' || (v.kind === 'null' && kinds && kinds.has('seq') && !kinds.has('map'));
    const count = kids.length;
    const open = isOpen(e, depth, count);
    const named = parent === root && e.type === 'pair' && NAMED_SECTIONS.has(e.key);
    const isNamedChild = parent && parent.type === 'pair' && doc.byId.get(parent.parent) === root && NAMED_SECTIONS.has(parent.key);
    const js = getAt(doc.js, e.path);
    let summary = '';
    if (!open) summary = count ? `${count} ${count === 1 ? 'entry' : 'entries'}` : 'empty';
    if (e.type === 'item') summary = itemSummary(fieldKey(e), js) || summary;
    rows.push({
      ...base, t: parent === root ? 'section' : 'group', label: label || summary || '(item)', open, count, summary,
      isService: isNamedChild && parent.key === 'services', canRename: !!isNamedChild || inFreeMap,
      itemLabel: e.type === 'item', convertible: false,
    });
    if (!open) return;
    for (const c of kids) emit(c, depth + 1);

    // What can be added here.
    const obj = fs !== undefined ? S.branch(fs, 'map') : null;
    const free = obj ? S.freeKeys(obj) : null;
    const add = { id: `${e.id}+`, t: 'add', entryId: e.id, depth: depth + 1 };
    if (isSeqLike) {
      const arr = fs !== undefined ? S.branch(fs, 'seq') : null;
      const items = arr && arr.items ? S.resolve(arr.items) : {};
      const itemKinds = S.kinds(items);
      rows.push({ ...add, addKind: !itemKinds || itemKinds.has('scalar') ? 'item' : 'objitem', label: 'Add item' });
    } else if (named) {
      const noun = { services: 'service', networks: 'network', volumes: 'volume', secrets: 'secret', configs: 'config', models: 'model' }[e.key];
      rows.push({ ...add, addKind: 'named', section: e.key, label: `Add ${noun}` });
    } else if (obj && obj.properties) {
      rows.push({ ...add, addKind: 'field', label: 'Add field' });
    } else if (free || !obj || S.isAny(obj)) {
      const vk = free ? S.kinds(free.schema) : null;
      rows.push({ ...add, addKind: !vk || vk.has('scalar') ? 'kv' : 'namedkv', label: 'Add entry' });
    }
  };

  for (const c of root.children) emit(c, 0);
  rows.push({ id: 'root+', t: 'add', addKind: 'field', entryId: 'root', depth: 0, label: 'Add top-level section' });
  return rows;
}

// pickerFields lists the fields that can be added to an entry, most common first, marking ones
// that are already there but commented out.
export function pickerFields(doc, S, entryId) {
  const e = doc.byId.get(entryId);
  if (!e) return [];
  const obj = S.branch(S.at(doc, e), 'map');
  const live = new Set(e.children.filter((c) => !c.disabled).map((c) => c.key));
  const off = new Map(e.children.filter((c) => c.disabled && !c.duplicate).map((c) => [c.key, c.id]));
  return S.fields(obj)
    .filter((f) => !live.has(f.key))
    .map((f) => ({ ...f, disabledId: off.get(f.key) || null, addKind: addKindFor(f.kinds, f.key) }));
}

// Fields that take a list or a mapping, where the mapping form reads better as key/value rows.
const DICT_FIRST = new Set(['environment', 'labels', 'args', 'annotations', 'sysctls', 'options', 'driver_opts']);

// addKindFor picks the empty value a newly added field starts with.
export function addKindFor(kinds, key) {
  if (!kinds || kinds.has('scalar')) return 'scalar';
  if (kinds.has('map') && (!kinds.has('seq') || DICT_FIRST.has(key))) return 'map';
  if (kinds.has('seq')) return 'seq';
  return 'map';
}
