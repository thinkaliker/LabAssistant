// Compose-spec schema helpers for the simple compose editor: what kind of value a field takes,
// which fields exist (with their descriptions) for the "Add field" picker, and whether a
// commented-out block plausibly is disabled config rather than a note.
//
// Driven by the vendored official schema (vendor/compose-spec.json). Pure; tested with node --test.

const PROSE_KEYS = /^(todo|fixme|note|notes|nb|warn(ing)?|important|info|examples?|usage|see|tip|hint|defaults?|optional|required|deprecated|moved|removed|why|how)$/i;

// Fields whose list items routinely contain spaces (a command line, KEY=some value).
const SPACED_ITEMS = new Set(['command', 'entrypoint', 'test']);
const KV_ITEMS = new Set(['environment', 'labels', 'args', 'annotations', 'sysctls', 'extra_hosts']);

// The common service fields, offered first in the picker.
const COMMON = ['image', 'build', 'container_name', 'restart', 'ports', 'volumes', 'environment', 'env_file',
  'depends_on', 'networks', 'command', 'entrypoint', 'labels', 'healthcheck', 'user', 'working_dir',
  'extra_hosts', 'devices', 'cap_add', 'logging', 'deploy', 'profiles'];

// Suggestions for free-text fields whose schema has no enum.
const HINTS = {
  restart: ['no', 'always', 'on-failure', 'unless-stopped'],
  pull_policy: ['always', 'never', 'missing', 'build', 'if_not_present', 'daily', 'weekly'],
  network_mode: ['bridge', 'host', 'none'],
  ipc: ['host', 'private', 'shareable'],
  pid: ['host'],
  driver: ['bridge', 'host', 'overlay', 'macvlan', 'local', 'json-file', 'journald', 'syslog', 'none'],
  condition: ['service_started', 'service_healthy', 'service_completed_successfully'],
};

const KIND_OF = { map: 'map', flowmap: 'map', seq: 'seq', flowseq: 'seq', scalar: 'scalar', block: 'scalar', null: 'null', alias: null };

export function createSchema(json) {
  const defs = json.$defs || json.definitions || {};
  const cache = new Map();

  // resolve follows $ref, letting the referencing site's description/deprecated win.
  const resolve = (node, depth = 0) => {
    if (!node || typeof node !== 'object' || depth > 20) return node || null;
    if (!node.$ref) return node;
    if (cache.has(node)) return cache.get(node);
    const name = String(node.$ref).replace(/^#\/(\$defs|definitions)\//, '');
    const target = resolve(defs[name], depth + 1) || {};
    const { $ref, ...site } = node;
    const out = Object.keys(site).length ? { ...target, ...site } : target;
    cache.set(node, out);
    return out;
  };

  const isAny = (n) => n && typeof n === 'object' && !n.type && !n.oneOf && !n.anyOf && !n.properties && !n.patternProperties && !n.items && !n.enum && !n.$ref;

  const typesOf = (n) => {
    if (Array.isArray(n.type)) return n.type;
    if (n.type) return [n.type];
    if (n.properties || n.patternProperties || n.additionalProperties) return ['object'];
    if (n.items) return ['array'];
    if (n.enum) return ['string'];
    return [];
  };

  // branch picks the part of a (possibly oneOf) schema describing a value of the given kind:
  // 'map', 'seq', 'scalar' or 'null'. null means the kind isn't allowed.
  const branch = (node, want) => {
    node = resolve(node);
    if (!node) return null;
    if (isAny(node)) return node;
    const alts = node.oneOf || node.anyOf;
    if (alts) {
      for (const b of alts) {
        const r = branch(b, want);
        if (r) return r;
      }
      return null;
    }
    const types = typesOf(node);
    if (!types.length) return node;
    if (want === 'scalar') return types.some((t) => t === 'string' || t === 'number' || t === 'integer' || t === 'boolean') ? node : null;
    return types.includes({ map: 'object', seq: 'array', null: 'null' }[want]) ? node : null;
  };

  // kinds lists the value kinds a field accepts, or null when anything goes.
  const kinds = (node) => {
    node = resolve(node);
    if (!node || isAny(node)) return null;
    const out = new Set();
    for (const k of ['map', 'seq', 'scalar', 'null']) if (branch(node, k)) out.add(k);
    return out;
  };

  // child is the schema for key inside an object schema: undefined when unknown to compose,
  // {} when anything is allowed.
  const child = (obj, key) => {
    if (!obj) return undefined;
    if (isAny(obj)) return {};
    if (obj.properties && Object.prototype.hasOwnProperty.call(obj.properties, key)) return resolve(obj.properties[key]);
    for (const [pat, sch] of Object.entries(obj.patternProperties || {})) {
      if (new RegExp(pat).test(key)) return resolve(sch) || {};
    }
    if (obj.additionalProperties === false) return undefined;
    if (obj.additionalProperties && typeof obj.additionalProperties === 'object') return resolve(obj.additionalProperties);
    return obj.properties ? undefined : {};
  };

  const memo = new WeakMap();
  // at returns the schema describing an entry's value (undefined: unknown field, {}: anything).
  const at = (doc, entry) => {
    if (!entry) return undefined;
    if (entry.type === 'root') return json;
    let m = memo.get(doc);
    if (!m) memo.set(doc, (m = new Map()));
    if (m.has(entry.id)) return m.get(entry.id);
    const parent = doc.byId.get(entry.parent);
    const ps = at(doc, parent);
    let out;
    if (ps !== undefined) {
      if (entry.type === 'pair') out = child(branch(ps, 'map'), entry.key);
      else {
        const arr = branch(ps, 'seq');
        out = arr ? (arr.items ? resolve(arr.items) : {}) : undefined;
      }
    }
    m.set(entry.id, out);
    return out;
  };

  // freeKeys describes a map whose keys are user-chosen (environment, labels, service names).
  const freeKeys = (obj) => {
    if (!obj || isAny(obj)) return null;
    for (const [pat, sch] of Object.entries(obj.patternProperties || {})) {
      if (pat !== '^x-') return { pattern: pat, schema: resolve(sch) || {} };
    }
    if (obj.additionalProperties && typeof obj.additionalProperties === 'object') return { pattern: '.+', schema: resolve(obj.additionalProperties) };
    return null;
  };

  const describe = (node) => {
    const r = resolve(node) || {};
    let enumVals = r.enum || null;
    if (!enumVals && r.oneOf) {
      for (const b of r.oneOf) {
        const rb = resolve(b);
        if (rb && rb.enum) enumVals = rb.enum;
      }
    }
    return { description: r.description || '', deprecated: !!r.deprecated, enum: enumVals };
  };

  const fields = (obj) => {
    if (!obj || !obj.properties) return [];
    const list = Object.entries(obj.properties).map(([key, sch]) => ({ key, ...describe(sch), kinds: kinds(sch) }));
    const rank = (k) => {
      const i = COMMON.indexOf(k);
      return i < 0 ? COMMON.length : i;
    };
    return list.sort((a, b) => rank(a.key) - rank(b.key) || a.key.localeCompare(b.key));
  };

  // scalarAllow says whether a numeric or boolean value may be written unquoted.
  const scalarAllow = (node) => {
    const sc = branch(node, 'scalar');
    if (!sc || isAny(sc)) return {};
    const types = typesOf(sc);
    const alts = (resolve(node) || {}).oneOf || [];
    const all = new Set([...types, ...alts.flatMap((b) => typesOf(resolve(b) || {}))]);
    return { number: all.has('number') || all.has('integer'), boolean: all.has('boolean') };
  };

  const suggestions = (key, node) => {
    const d = describe(node);
    return d.enum ? d.enum.map(String) : HINTS[key] || null;
  };

  // containerKey reports the key of the list/map a list item or map entry lives in (ports, labels).
  const fieldKey = (doc, ctx) => {
    for (let e = ctx; e && e.type !== 'root'; e = doc.byId.get(e.parent)) if (e.type === 'pair') return e.key;
    return null;
  };

  // accept decides whether a commented-out block is disabled config at ctx. Prose that happens to
  // parse as YAML (`# Note: …`, `# - remember to …`) is rejected by name, by schema, and by shape.
  const accept = (ctx, cand, doc) => {
    const cs = at(doc, ctx);
    if (cand.type === 'pair') {
      if (/\s/.test(cand.key)) return false;
      if (cand.key === '<<') return cand.valueKind === 'alias' || cand.valueKind === 'seq' || cand.valueKind === 'flowseq';
      if (cs === undefined) return !PROSE_KEYS.test(cand.key) && /^[A-Za-z0-9_.\-/]+$/.test(cand.key);
      const obj = branch(cs, 'map');
      if (!obj) return false;
      let fs;
      // A real field name wins over the prose list (depends_on has a `required` field).
      if (obj.properties && Object.prototype.hasOwnProperty.call(obj.properties, cand.key)) fs = resolve(obj.properties[cand.key]);
      else if (PROSE_KEYS.test(cand.key)) return false;
      else if (/^x-/.test(cand.key) && obj.patternProperties && obj.patternProperties['^x-']) fs = {};
      else {
        const free = freeKeys(obj);
        if (!free) return isAny(obj) && /^[A-Za-z0-9_.\-/]+$/.test(cand.key);
        if (!new RegExp(free.pattern).test(cand.key)) return false;
        // Catch-all dicts (environment, labels, options): real keys are ENV_STYLE or dotted. A plain
        // word key still counts when its value is a single token (`mode: 0755`), unlike prose
        // (`MariaDB: small footprint, …`).
        if ((free.pattern === '.+' || free.pattern === '^.+$') && !/^[A-Z0-9_]+$/.test(cand.key) && !/[._\-/]/.test(cand.key)) {
          return cand.valueKind === 'scalar' && !/\s/.test(cand.scalar || '');
        }
        fs = free.schema;
      }
      const ks = kinds(fs);
      if (!ks) return true;
      const k = KIND_OF[cand.valueKind];
      if (k === 'null') return ks.has('null') || ks.has('map') || ks.has('seq');
      return k == null || ks.has(k);
    }
    const arr = cs === undefined ? null : branch(cs, 'seq');
    if (cs !== undefined && !arr) return false;
    const is = arr && arr.items ? resolve(arr.items) : {};
    const ks = kinds(is);
    const k = KIND_OF[cand.valueKind];
    if (ks && k != null && k !== 'null' && !ks.has(k)) return false;
    if (k === 'map' && cand.js && typeof cand.js === 'object') {
      // A long-syntax item (`- target: 80`) must use that list's field names; `- Note: …` is prose.
      const obj = branch(is, 'map');
      if (obj && !isAny(obj) && !Object.keys(cand.js).every((key) => child(obj, key) !== undefined)) return false;
    }
    if (cand.valueKind === 'scalar' && /\s/.test(cand.scalar || '')) {
      const field = fieldKey(doc, ctx);
      if (SPACED_ITEMS.has(field)) return true;
      if (KV_ITEMS.has(field)) return /^[^=\s]+(=|$)/.test(cand.scalar);
      // Config with spaces still has structure (`c 189:* rwm`, `/mnt/My Media:/media`); a
      // bullet of plain words doesn't.
      return /[:=/]/.test(cand.scalar);
    }
    return true;
  };

  // enableValue gives a key-only disabled header (`# ports:`) an empty value compose accepts.
  const enableValue = (ctx, d, doc) => {
    const fs = child(branch(at(doc, ctx), 'map'), d.key);
    const ks = kinds(fs);
    if (!ks || ks.has('null') || ks.has('scalar')) return null;
    if (ks.has('map') && ks.has('seq')) {
      // Pick the form of whatever was commented out under it, inside the block or right after it.
      const inner = d.fragment.split('\n').slice(1).find((l) => l.trim() !== '') || '';
      const next = doc.lines[d.endLine + 1] || '';
      return /^\s*(#\s*)?-(\s|$)/.test(inner) || /^\s*#\s*-(\s|$)/.test(next) ? '[]' : '{}';
    }
    return ks.has('seq') ? '[]' : ks.has('map') ? '{}' : null;
  };

  const allowTypes = (entry, doc) => scalarAllow(at(doc, entry));

  const allowTypesFor = (parent, key, doc) => {
    const ps = at(doc, parent);
    if (key == null) {
      const arr = branch(ps, 'seq');
      return scalarAllow(arr && arr.items ? resolve(arr.items) : {});
    }
    return scalarAllow(child(branch(ps, 'map'), key));
  };

  // checkKey rejects a name compose wouldn't accept at that position.
  const checkKey = (parent, key, doc) => {
    const obj = branch(at(doc, parent), 'map');
    if (!obj || isAny(obj)) return null;
    if (child(obj, key) !== undefined) return null;
    const free = freeKeys(obj);
    if (free) return `"${key}" isn't a valid name here (allowed: ${free.pattern}).`;
    return `Compose doesn't have a "${key}" field here.`;
  };

  // unknownKeys lists live keys compose will reject, for the editor's warnings.
  const unknownKeys = (doc) => {
    const out = [];
    for (const e of doc.entries) {
      if (e.type !== 'pair') continue;
      const ps = at(doc, doc.byId.get(e.parent));
      if (ps !== undefined && at(doc, e) === undefined && !doc.entries.some((p) => p.id === e.parent && p.readonly)) {
        out.push(e);
      }
    }
    return out;
  };

  return {
    json, resolve, branch, kinds, child, at, freeKeys, fields, describe, scalarAllow, suggestions,
    isAny, accept, enableValue, allowTypes, allowTypesFor, checkKey, unknownKeys,
    hooks: { accept, enableValue, allowTypes, allowTypesFor, checkKey },
  };
}
