// Compose document engine behind the simple compose editor.
//
// The YAML text stays the source of truth. load() projects it into entries — fields, list items and
// "disabled" entries (config commented out with `#`) — and apply() runs operations as minimal line
// edits on the text. After every edit the text is re-parsed and the parsed data must have changed
// exactly as the operation intended; anything else (a patch that happens to parse but means
// something different) is refused. Formatting and comments the user didn't touch are never
// rewritten, so switching between this and the raw YAML editor is lossless.
//
// Pure: no DOM or Alpine, so node --test can exercise it (dashboard/jstest). Loaded lazily by
// composeform.js, since it pulls in the YAML library.

import { parseDocument, Parser, isMap, isSeq, isScalar, isAlias, isPair, visit } from '../vendor/yaml-2.9.1.min.js';

const SAFE = "Couldn't apply that change safely — make it in the YAML view instead.";
const CTRL = /[\u0000-\u0008\u000b-\u001f\u007f\u0085\u2028\u2029]/;

// OpError carries a message meant for the user (as opposed to an internal failure).
export class OpError extends Error {}

// normalize strips a BOM and converts CRLF/CR line endings to LF — the same normalization the
// CodeMirror editor already applies when it saves.
export function normalize(raw) {
  let text = String(raw ?? '');
  const hadBOM = text.charCodeAt(0) === 0xfeff;
  if (hadBOM) text = text.slice(1);
  const hadCRLF = /\r/.test(text);
  if (hadCRLF) text = text.replace(/\r\n?/g, '\n');
  return { text, hadBOM, hadCRLF };
}

// ---------------------------------------------------------------------------------------------
// load

// load parses text into a Doc. hooks.accept(ctx, candidate, doc) decides whether a commented-out
// block really is disabled config (composeschema supplies the compose-aware version). A Doc that
// can't be shown as a form carries a `blocked` reason and no entries.
export function load(text, hooks = {}) {
  const src = normalize(text).text;
  const lines = src.split('\n');
  const lineStarts = [0];
  for (let i = 0; i < src.length; i++) if (src.charCodeAt(i) === 10) lineStarts.push(i + 1);
  const doc = {
    text: src, lines, lineStarts, root: null, byId: new Map(), entries: [], disabled: [],
    indentUnit: 2, seqStyle: 'indented', js: null, blocked: '', anchors: [], aliases: [],
  };
  const lineOf = (off) => {
    let lo = 0, hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= off) lo = mid; else hi = mid - 1;
    }
    return lo;
  };
  const colOf = (off) => off - lineStarts[lineOf(off)];
  doc.lineOf = lineOf;
  doc.colOf = colOf;

  let ydoc;
  try {
    ydoc = parseDocument(src);
  } catch (e) {
    doc.blocked = 'The YAML could not be parsed: ' + e.message;
    return doc;
  }
  if (ydoc.errors.length) {
    doc.blocked = 'The YAML has an error: ' + String(ydoc.errors[0].message).split('\n')[0];
    return doc;
  }
  const contents = ydoc.contents;
  if (contents != null && !(isMap(contents) && !contents.flow)) {
    doc.blocked = 'The file must be a YAML mapping (key: value lines) at the top level.';
    return doc;
  }
  try {
    doc.js = ydoc.toJS() ?? null;
  } catch (e) {
    doc.blocked = 'The YAML can’t be resolved: ' + e.message;
    return doc;
  }

  // The CST supplies what the composed nodes don't: comment positions and the `-` of each list item
  // (a list item's node range starts at its value, not at its dash).
  const commentOffsets = [];
  const dashes = [];
  let docStart = -1;
  const walk = (tok, inFlow) => {
    if (Array.isArray(tok)) {
      for (const t of tok) walk(t, inFlow);
      return;
    }
    if (!tok || typeof tok !== 'object') return;
    if (tok.type === 'comment') {
      if (!inFlow) commentOffsets.push(tok.offset);
      return;
    }
    if (tok.type === 'seq-item-ind') dashes.push(tok.offset);
    if (tok.type === 'doc-start' && docStart < 0) docStart = tok.offset;
    if (tok.type === 'flow-collection') {
      // Comments between the items are inside the inline collection; everything after its closing
      // bracket (which the CST also files under `end`) is not.
      walk(tok.start, inFlow);
      walk(tok.items, true);
      let inside = true;
      for (const t of tok.end || []) {
        walk(t, inFlow || inside);
        if (t.type === 'flow-seq-end' || t.type === 'flow-map-end') inside = false;
      }
      return;
    }
    for (const k in tok) {
      const v = tok[k];
      if (v && typeof v === 'object') walk(v, inFlow);
    }
  };
  walk([...new Parser().parse(src)], false);
  dashes.sort((a, b) => a - b);
  const dashBefore = (off) => {
    let lo = 0, hi = dashes.length - 1, best = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (dashes[mid] < off) { best = dashes[mid]; lo = mid + 1; } else hi = mid - 1;
    }
    return best;
  };

  visit(ydoc, {
    Node(_, node) {
      if (node.anchor && node.range) doc.anchors.push({ name: node.anchor, offset: node.range[0] });
    },
    Alias(_, node) {
      if (node.range) doc.aliases.push({ name: node.source, offset: node.range[0] });
    },
  });

  // afterProps returns the offset just past `from` and any anchor/tag that follows on the same line:
  // where a value (or an empty `{}` / `[]`) goes when one is inserted.
  const afterProps = (from) => {
    let pos = from;
    for (;;) {
      let q = pos;
      while (src[q] === ' ' || src[q] === '\t') q++;
      if (src[q] === '&' || src[q] === '!') {
        while (q < src.length && !/\s/.test(src[q])) q++;
        pos = q;
      } else return pos;
    }
  };

  const root = {
    id: 'root', type: 'root', path: [], parent: null, depth: 0, col: -1, line: 0,
    endLine: lines.length - 1, spanEnd: lines.length - 1, children: [], readonly: null,
    key: null, insertAt: 0,
  };
  doc.root = root;
  doc.byId.set(root.id, root);

  const valueEnd = (node) => {
    if (node == null) return -1;
    if (isScalar(node) || isAlias(node) || node.flow) return node.range[1];
    const last = node.items[node.items.length - 1];
    if (!last) return node.range[0];
    if (isPair(last)) {
      const v = valueEnd(last.value);
      return Math.max(v, last.key && last.key.range ? last.key.range[1] : -1);
    }
    return valueEnd(last);
  };

  const describe = (node) => {
    if (node == null) return { kind: 'null', range: null, empty: true };
    const range = node.range;
    const raw = src.slice(range[0], range[1]);
    const common = { range, tag: node.tag || null, anchor: node.anchor || null };
    if (isAlias(node)) return { ...common, kind: 'alias', display: raw, singleLine: !raw.includes('\n') };
    if (isScalar(node)) {
      if (raw === '') return { ...common, kind: 'null', empty: true };
      if (node.type === 'BLOCK_LITERAL' || node.type === 'BLOCK_FOLDED') {
        return { ...common, kind: 'block', style: node.type, display: String(node.value), jsType: 'string', singleLine: false };
      }
      const single = !raw.includes('\n');
      return {
        ...common, kind: 'scalar', style: node.type, singleLine: single,
        display: node.type === 'PLAIN' && single ? raw : String(node.value), jsType: node.value === null ? 'null' : typeof node.value,
      };
    }
    const isM = isMap(node);
    if (node.flow) {
      const single = !raw.includes('\n');
      const v = { ...common, kind: isM ? 'flowmap' : 'flowseq', empty: node.items.length === 0, singleLine: single, items: null };
      // Inline lists/maps of plain values are editable item by item; anything nested is not.
      const scalarOk = (n) => isScalar(n) && n.type !== 'BLOCK_LITERAL' && n.type !== 'BLOCK_FOLDED' && !n.tag && !n.anchor;
      if (single && node.items.every((it) => (isM ? isPair(it) && scalarOk(it.key) && (it.value == null || scalarOk(it.value)) : scalarOk(it)))) {
        v.items = node.items.map((it) => {
          if (!isM) return { src: src.slice(it.range[0], it.range[1]), value: String(it.value), style: it.type };
          const val = it.value;
          return {
            keySrc: src.slice(it.key.range[0], it.key.range[1]), key: String(it.key.value),
            src: val ? src.slice(val.range[0], val.range[1]) : '', value: val ? String(val.value ?? '') : '', style: val ? val.type : 'PLAIN',
          };
        });
      }
      return v;
    }
    return { ...common, kind: isM ? 'map' : 'seq', node, childCol: null, seqStyle: null };
  };

  const add = (entry) => {
    doc.byId.set(entry.id, entry);
    doc.entries.push(entry);
  };

  const build = (parent, node) => {
    if (isMap(node)) {
      for (const pair of node.items) {
        const k = pair.key;
        if (!k || !k.range) continue;
        const keyStart = k.range[0];
        const line = lineOf(keyStart);
        const keyIsScalar = isScalar(k);
        const key = keyIsScalar ? String(k.value) : src.slice(k.range[0], k.range[1]);
        const e = {
          id: 'e:' + JSON.stringify([...parent.path, key]), type: 'pair', path: [...parent.path, key],
          parent: parent.id, depth: parent.depth + 1, key, keySrc: src.slice(k.range[0], k.range[1]),
          keyRange: [k.range[0], k.range[1]], col: colOf(keyStart), line, endLine: line, spanEnd: line,
          onDashLine: parent.type === 'item' && parent.line === line, children: [], readonly: null,
          insertAt: -1,
        };
        let colon = k.range[1];
        while (src[colon] === ' ' || src[colon] === '\t') colon++;
        const explicitKey = src.slice(lineStarts[line], keyStart).trimEnd().endsWith('?');
        if (src[colon] === ':' && !explicitKey) e.insertAt = afterProps(colon + 1);
        e.value = describe(pair.value);
        if (!keyIsScalar || explicitKey) e.readonly = 'complex key';
        else if (k.anchor || k.tag) e.readonly = 'key with an anchor or tag';
        else if (key === '<<') e.readonly = 'YAML merge key';
        else if (e.value.kind === 'alias') e.readonly = 'YAML alias';
        else if ((e.value.kind === 'flowmap' || e.value.kind === 'flowseq') && !e.value.items && !e.value.empty) e.readonly = 'inline list';
        const end = Math.max(valueEnd(pair.value), k.range[1]);
        e.endLine = e.spanEnd = Math.max(line, lineOf(Math.max(end - 1, keyStart)));
        add(e);
        parent.children.push(e);
        if ((e.value.kind === 'map' || e.value.kind === 'seq') && !e.readonly) build(e, pair.value);
      }
    } else {
      node.items.forEach((item, i) => {
        const start = item.range[0];
        const dash = dashBefore(start);
        if (dash < 0) return;
        const line = lineOf(dash);
        const e = {
          id: 'e:' + JSON.stringify([...parent.path, i]), type: 'item', path: [...parent.path, i], index: i,
          parent: parent.id, depth: parent.depth + 1, key: null, col: colOf(dash), line, endLine: line,
          spanEnd: line, dashOffset: dash, insertAt: afterProps(dash + 1), onDashLine: false,
          children: [], readonly: null,
        };
        e.value = describe(item);
        if (e.value.kind === 'alias') e.readonly = 'YAML alias';
        else if (e.value.kind === 'seq' && lineOf(item.range[0]) === line) e.readonly = 'nested list';
        else if ((e.value.kind === 'flowmap' || e.value.kind === 'flowseq') && !e.value.items && !e.value.empty) e.readonly = 'inline list';
        e.endLine = e.spanEnd = Math.max(line, lineOf(Math.max(valueEnd(item) - 1, dash)));
        add(e);
        parent.children.push(e);
        if ((e.value.kind === 'map' || e.value.kind === 'seq') && !e.readonly) build(e, item);
      });
    }
    if (parent.value && parent.children.length) parent.value.childCol = parent.children[0].col;
  };

  root.value = contents ? { kind: 'map', node: contents, childCol: null, range: contents.range } : { kind: 'null', empty: true, childCol: null };
  if (contents) build(root, contents);
  if (root.value.childCol == null) root.value.childCol = 0;

  // Layout conventions, so added lines look like their neighbours.
  const units = new Map();
  let indentless = 0, indented = 0;
  for (const e of doc.entries) {
    if (e.type !== 'pair' || !e.children.length) continue;
    const d = e.value.childCol - e.col;
    if (e.value.kind === 'seq') {
      if (d === 0) indentless++; else indented++;
      e.value.seqStyle = d === 0 ? 'indentless' : 'indented';
    }
    if (d > 0 && e.value.kind === 'map') units.set(d, (units.get(d) || 0) + 1);
  }
  if (units.size) doc.indentUnit = [...units.entries()].sort((a, b) => b[1] - a[1] || a[0] - b[0])[0][0];
  doc.seqStyle = indentless > indented ? 'indentless' : 'indented';

  detectDisabled(doc, hooks, commentOffsets.filter((o) => o > docStart));

  for (const e of [root, ...doc.entries]) e.children.sort((a, b) => a.line - b.line);
  return doc;
}

// ---------------------------------------------------------------------------------------------
// disabled (commented-out) entries

const defaultAccept = (ctx, cand) => cand.type === 'item' || /^\S+$/.test(cand.key);

function detectDisabled(doc, hooks, commentOffsets) {
  const { lines, lineOf, colOf } = doc;
  const hashCols = new Map();
  for (const off of commentOffsets) {
    const ln = lineOf(off);
    const col = colOf(off);
    if (lines[ln].slice(0, col).trim() === '' && !hashCols.has(ln)) hashCols.set(ln, col);
  }
  if (!hashCols.size) return;

  // Runs: comment-only lines sharing a `#` column, bridged across blank lines.
  const runs = [];
  let run = null;
  for (let ln = 0; ln < lines.length; ln++) {
    const hc = hashCols.get(ln);
    if (hc !== undefined) {
      if (run && run.hashCol === hc) {
        for (let b = run.last + 1; b < ln; b++) run.lines.push(b);
      } else {
        run = { hashCol: hc, lines: [], last: ln };
        runs.push(run);
      }
      run.lines.push(ln);
      run.last = ln;
    } else if (lines[ln].trim() !== '') {
      run = null;
    }
  }

  const accept = hooks.accept || defaultAccept;
  const enabled = doc.entries;
  const lastBefore = (line) => {
    let lo = 0, hi = enabled.length - 1, best = null;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (enabled[mid].line < line) { best = enabled[mid]; lo = mid + 1; } else hi = mid - 1;
    }
    return best;
  };
  const firstAfter = (line) => {
    let lo = 0, hi = enabled.length - 1, best = null;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (enabled[mid].line > line) { best = enabled[mid]; hi = mid - 1; } else lo = mid + 1;
    }
    return best;
  };
  const counters = new Map();

  for (const r of runs) {
    const rows = r.lines.map((ln) => {
      const text = lines[ln];
      if (text.trim() === '') return { line: ln, gap: true };
      const m = /^( *)(.*)$/.exec(text.slice(r.hashCol + 1));
      if (m[2].trim() === '') return { line: ln, gap: true };
      return { line: ln, gap: false, bodyCol: r.hashCol + 1 + m[1].length, content: m[2] };
    });
    let i = 0;
    while (i < rows.length) {
      while (i < rows.length && rows[i].gap) i++;
      if (i >= rows.length) break;
      const b0 = rows[i].bodyCol;
      const shape = lineShape(rows[i].content);
      // `ports:` with nothing after it can own an indentless list: `- x` lines at its own column.
      const header = /^[^\s#'"-][^:]*:\s*$/.test(rows[i].content);
      const member = (r) => {
        if (r.gap) return true;
        if (r.bodyCol === b0) return header && (r.content.startsWith('-') || r.content.startsWith('#'));
        // Deeper lines belong to this entry — except one exactly a column deeper with the same
        // shape, which is a sibling commented in the other style (`#- a` above `# - b`).
        return r.bodyCol > b0 && !(r.bodyCol === b0 + 1 && shape && lineShape(r.content) === shape);
      };
      let k = i + 1;
      while (k < rows.length && member(rows[k])) k++;
      let end = k - 1;
      while (rows[end].gap) end--;
      // Gap-delimited prefixes, longest first: a disabled block often runs straight into prose.
      const ends = [];
      for (let j = i; j <= end; j++) if (!rows[j].gap && (j === end || rows[j + 1].gap)) ends.push(j);
      let accepted = -1;
      for (let s = ends.length - 1; s >= 0 && accepted < 0 && ends.length - s <= 12; s--) {
        let last = ends[s];
        let cand = candidate(doc, rows.slice(i, last + 1), r.hashCol, b0);
        if (!cand) continue;
        if (cand.keep) {
          // A `|+` block keeps its trailing blank lines, which were commented out as bare `#`.
          let j = last + 1;
          while (j < rows.length && rows[j].gap && lines[rows[j].line].trim() !== '') j++;
          if (j > last + 1) {
            const longer = candidate(doc, rows.slice(i, j), r.hashCol, b0);
            if (longer) {
              cand = longer;
              last = j - 1;
            }
          }
        }
        const placed = place(doc, cand, lastBefore, firstAfter, accept);
        if (!placed) continue;
        const { ctx, L } = placed;
        const ord = `${ctx.id}:${cand.key ?? '-'}`;
        const n = (counters.get(ord) || 0) + 1;
        counters.set(ord, n);
        const d = {
          ...cand, id: `d:${ord}#${n}`, disabled: true, parent: ctx.id, depth: ctx.depth + 1, L, col: L,
          spanEnd: cand.endLine, children: [], readonly: null,
          duplicate: cand.type === 'pair' && ctx.children.some((c) => !c.disabled && c.key === cand.key),
        };
        doc.byId.set(d.id, d);
        doc.disabled.push(d);
        ctx.children.push(d);
        for (let a = ctx; a && a.type !== 'root'; a = doc.byId.get(a.parent)) a.spanEnd = Math.max(a.spanEnd, d.endLine);
        accepted = last;
      }
      // Not config: skip only the first line, so a real disabled block right under a prose line
      // (indented deeper) still gets its own chance.
      i = accepted >= 0 ? accepted + 1 : i + 1;
    }
  }
}

const lineShape = (content) => (/^-(\s|$)/.test(content) ? 'item' : /^[^\s#'"-][^:]*:(\s|$)/.test(content) ? 'key' : null);

// candidate re-indents comment rows into a YAML fragment and accepts it only if it parses cleanly
// to exactly one mapping entry or one list item.
function candidate(doc, rows, hashCol, b0) {
  const text = rows.map((r) => (r.gap ? '' : ' '.repeat(r.bodyCol - b0) + r.content)).join('\n') + '\n';
  let d;
  try {
    d = parseDocument(text);
  } catch {
    return null;
  }
  if (d.errors.length) return null;
  const c = d.contents;
  let type, key = null, node;
  if (isMap(c) && !c.flow && c.items.length === 1 && isScalar(c.items[0].key) && !c.items[0].key.tag && !c.items[0].key.anchor) {
    type = 'pair';
    key = String(c.items[0].key.value);
    node = c.items[0].value;
    const ks = c.items[0].key.range[0];
    if (text.slice(0, ks).trim() !== '') return null; // explicit `? key`
  } else if (isSeq(c) && !c.flow && c.items.length === 1) {
    type = 'item';
    node = c.items[0];
  } else return null;
  let js, aliases = false;
  try {
    js = d.toJS();
    js = type === 'pair' ? js[key] : js[0];
  } catch {
    aliases = true; // refers to an anchor in the live document; enable is checked loosely
  }
  const kind = node == null || (isScalar(node) && node.range && node.range[0] === node.range[1] && node.value == null)
    ? 'null'
    : isAlias(node) ? 'alias'
    : isScalar(node) ? (node.type === 'BLOCK_LITERAL' || node.type === 'BLOCK_FOLDED' ? 'block' : 'scalar')
    : isMap(node) ? (node.flow ? 'flowmap' : 'map') : (node.flow ? 'flowseq' : 'seq');
  const first = rows[0].content;
  let summary = first;
  if (kind === 'scalar' || kind === 'block') summary = type === 'pair' ? `${key}: ${String(isScalar(node) ? node.value : '')}` : String(node.value);
  return {
    type, key, valueKind: kind, js, aliases, hashCol, b0, rows,
    line: rows[0].line, endLine: rows[rows.length - 1].line, fragment: text, summary,
    scalar: kind === 'scalar' ? String(node.value) : null,
    keep: kind === 'block' && /^[|>][1-9]?\+|^[|>]\+/.test(text.slice(node.range[0])),
  };
}

const kindOk = (ctx, type) => {
  const k = ctx.value ? ctx.value.kind : 'null';
  if (ctx.type === 'root') return type === 'pair';
  if (k === 'map') return type === 'pair';
  if (k === 'flowmap') return ctx.value.empty && type === 'pair';
  if (k === 'seq') return type === 'item';
  if (k === 'flowseq') return ctx.value.empty && type === 'item';
  return k === 'null';
};

// place finds which container a disabled candidate belongs to and at which column L it would sit
// once enabled. The `#` can be at the entry's own column (`  # key:`), at column 0 (sed), or with no
// space after it, so a few logical columns are tried.
function place(doc, cand, lastBefore, firstAfter, accept) {
  const b0 = cand.b0;
  const Ls = [];
  if (cand.hashCol === b0 - 2 || cand.hashCol === b0 - 1) Ls.push(cand.hashCol);
  Ls.push(b0 - 2, b0 - 1, b0);
  for (const L of [...new Set(Ls)].filter((x) => x >= 0)) {
    const ctx = findContext(doc, cand, L, lastBefore);
    if (!ctx) continue;
    const next = firstAfter(cand.endLine);
    if (next && next.col > L) continue; // would swallow the following live lines
    if (!accept(ctx, cand, doc)) continue;
    return { ctx, L };
  }
  return null;
}

function findContext(doc, cand, L, lastBefore) {
  const root = doc.root;
  let x = lastBefore(cand.line);
  if (!x) return L === root.value.childCol && kindOk(root, cand.type) ? root : null;
  while (x) {
    if (x.type === 'root') return L === root.value.childCol && kindOk(root, cand.type) ? root : null;
    const parent = doc.byId.get(x.parent);
    if (x.col === L) {
      if (kindOk(parent, cand.type)) return parent;
      // A list item under a key at the same column: an indentless list, or a list that is
      // currently empty (`ports: []`) or null.
      if (cand.type === 'item' && x.type === 'pair' && (x.value.kind === 'null' || (x.value.kind === 'flowseq' && x.value.empty) || (x.value.kind === 'seq' && x.value.seqStyle === 'indentless'))) return x;
      x = parent;
      continue;
    }
    if (x.col < L) {
      if (!x.value || x.readonly || !kindOk(x, cand.type)) return null;
      const cc = x.value.childCol;
      return (cc != null ? cc === L : L > x.col) ? x : null;
    }
    x = parent;
  }
  return null;
}

// ---------------------------------------------------------------------------------------------
// apply

const SAME = (js) => js;

// apply runs one operation or a list of them (one undo step) and returns the new Doc, or an error
// the UI can show. Operations address entries by id; ids come from the Doc they are applied to.
export function apply(doc, ops, hooks = {}) {
  if (doc.blocked) return { ok: false, error: doc.blocked };
  const list = Array.isArray(ops) ? ops : [ops];
  let cur = doc;
  const warnings = [];
  for (const op of list) {
    const fn = OPS[op && op.op];
    if (!fn) return { ok: false, error: 'Unknown operation.' };
    let r;
    try {
      r = fn(cur, op, hooks);
    } catch (e) {
      if (e instanceof OpError) return { ok: false, error: e.message };
      return { ok: false, error: SAFE, bug: e }; // `bug` lets tests tell a crash from a refusal
    }
    if (r.text === cur.text) continue;
    const next = verifyEdit(cur, r, hooks);
    if (!next) return { ok: false, error: SAFE };
    if (r.warning) warnings.push(r.warning);
    cur = next;
  }
  return { ok: true, doc: cur, text: cur.text, warnings };
}

// verifyEdit re-parses an edit's text and returns the new Doc only if the data changed exactly as
// the edit declared: edit.expect(data) → expected data, or edit.loosePath when the inserted value
// can't be computed on its own (it refers to anchors elsewhere) and only the rest is compared.
export function verifyEdit(doc, edit, hooks = {}) {
  const next = load(edit.text, hooks);
  if (next.blocked) return null;
  if (edit.loosePath) {
    const got = structuredClone(next.js);
    if (getAt(got, edit.loosePath) === undefined) return null;
    deleteAt(got, edit.loosePath);
    return canon(got) === canon(doc.js) ? next : null;
  }
  return canon(next.js) === canon(edit.expect(structuredClone(doc.js))) ? next : null;
}

function entryOf(doc, id) {
  const e = doc.byId.get(id);
  if (!e) throw new OpError('That item changed underneath the editor — try again.');
  return e;
}

const parentOf = (doc, e) => doc.byId.get(e.parent);
const enabledChildren = (e) => e.children.filter((c) => !c.disabled);

function commentOut(lines, from, to, col) {
  for (let i = from; i <= to; i++) {
    const l = lines[i];
    if (l.trim() === '') {
      lines[i] = ' '.repeat(col) + '#';
      continue;
    }
    if (l.slice(0, col).trim() !== '') {
      throw new OpError("Can't comment this out: a line inside it is less indented. Make the change in the YAML view.");
    }
    lines[i] = l.slice(0, col) + '# ' + l.slice(col);
  }
}

// emptyPlaceholder adds ` {}` / ` []` after a container's key (or dash) once its last live child is
// gone: compose rejects `ports:` with nothing under it, but accepts `ports: []`.
function emptyPlaceholder(doc, lines, P) {
  const kind = P.value.kind;
  const token = kind === 'seq' || kind === 'flowseq' ? '[]' : '{}';
  const line = P.line;
  const col = doc.colOf(P.insertAt);
  lines[line] = lines[line].slice(0, col) + ' ' + token + lines[line].slice(col);
}

// splitDashLine moves the first key of a `- key: value` item onto its own line, so the key's lines
// can be commented out without taking the dash with them.
function splitDashLine(doc, lines, E) {
  const P = parentOf(doc, E);
  const text = lines[E.line];
  if (doc.colOf(P.insertAt) !== P.col + 1) {
    throw new OpError("Can't split this list item (it has an anchor or tag). Make the change in the YAML view.");
  }
  lines.splice(E.line, 1, text.slice(0, E.col).trimEnd(), ' '.repeat(E.col) + text.slice(E.col));
}

function checkAnchors(doc, E) {
  const start = E.onDashLine ? E.keyRange[0] : doc.lineStarts[E.line];
  const endLine = E.spanEnd + 1;
  const end = endLine < doc.lineStarts.length ? doc.lineStarts[endLine] : doc.text.length;
  const inside = new Set(doc.anchors.filter((a) => a.offset >= start && a.offset < end).map((a) => a.name));
  const used = doc.aliases.find((a) => inside.has(a.name) && (a.offset < start || a.offset >= end));
  if (used) {
    throw new OpError(`This defines the YAML anchor &${used.name}, which is used elsewhere (*${used.name}). Make the change in the YAML view.`);
  }
}

function opDisable(doc, { id }) {
  const E = entryOf(doc, id);
  if (E.disabled || E.type === 'root') throw new OpError('That is already disabled.');
  const P = parentOf(doc, E);
  const live = enabledChildren(P);
  if (P.type === 'root' && live.length === 1) {
    throw new OpError("Can't disable the last top-level entry: compose rejects a file with nothing in it.");
  }
  checkAnchors(doc, E);
  const lines = doc.lines.slice();
  let from = E.line, to = E.spanEnd;
  if (E.onDashLine) {
    splitDashLine(doc, lines, E);
    from++;
    to++;
  }
  commentOut(lines, from, to, E.col);
  if (live.length === 1 && P.type !== 'root') emptyPlaceholder(doc, lines, P);
  return { text: lines.join('\n'), expect: (js) => deleteAt(js, E.path) };
}

function opEnable(doc, { id }, hooks) {
  const D = entryOf(doc, id);
  if (!D.disabled) throw new OpError('That is already enabled.');
  if (D.duplicate) throw new OpError(`"${D.key}" is already set here. Remove one of the two first.`);
  const ctx = parentOf(doc, D);
  const lines = doc.lines.slice();
  for (const r of D.rows) lines[r.line] = r.gap ? '' : ' '.repeat(D.L + r.bodyCol - D.b0) + r.content;
  let value = D.js;
  if (D.type === 'pair' && D.valueKind === 'null' && hooks.enableValue) {
    const ev = hooks.enableValue(ctx, D, doc); // '[]' / '{}' for a key-only header of a list/map field
    if (ev) {
      const first = D.rows.find((r) => !r.gap).line;
      lines[first] = lines[first].replace(/\s*$/, '') + ' ' + ev;
      value = ev === '[]' ? [] : {};
    }
  }
  let rejoinLine = -1;
  if ((ctx.value.kind === 'flowseq' || ctx.value.kind === 'flowmap') && ctx.value.empty) {
    const [s, e] = ctx.value.range;
    const ln = doc.lineOf(s);
    if (doc.lineOf(e - 1) !== ln) throw new OpError(SAFE);
    let a = doc.colOf(s);
    const b = doc.colOf(e);
    while (a > 0 && lines[ln][a - 1] === ' ') a--;
    lines[ln] = lines[ln].slice(0, a) + lines[ln].slice(b);
  }
  if (ctx.type === 'item' && D.type === 'pair' && /^\s*-\s*$/.test(lines[ctx.line]) && D.line === ctx.line + 1 && D.L === ctx.col + 2) {
    rejoinLine = ctx.line;
  }
  if (rejoinLine >= 0) {
    lines[rejoinLine] = lines[rejoinLine].trimEnd() + ' ' + lines[D.line].trimStart();
    lines.splice(D.line, 1);
  }
  const text = lines.join('\n');
  const where = D.type === 'item' ? ctx.children.filter((c) => !c.disabled && c.line < D.line).length : D.key;
  if (D.aliases) return { text, loosePath: [...ctx.path, where] };
  return { text, expect: (js) => insertAt(js, ctx.path, where, value) };
}

function opSet(doc, { id, value }, hooks) {
  const E = entryOf(doc, id);
  if (E.disabled || E.type === 'root') throw new OpError('Enable it before editing.');
  const v = E.value;
  if (E.readonly || !['null', 'scalar', 'block'].includes(v.kind)) throw new OpError('This value can only be edited in the YAML view.');
  const s = String(value ?? '');
  if (v.kind !== 'null' && s === v.display) return { text: doc.text, expect: SAME };
  const indent = E.type === 'item' ? E.col + 2 : E.col + doc.indentUnit;
  const allow = (hooks.allowTypes && hooks.allowTypes(E, doc)) || {};
  // An empty `""` is usually the placeholder a newly added field started with, not a quoting
  // choice worth keeping.
  const style = v.kind === 'scalar' && v.display !== '' ? v.style : null;
  const formatted = formatScalar(s, { ctx: E.type === 'item' ? 'item' : 'value', style, allow, indent });
  const src = doc.text;
  const multi = formatted.includes('\n');
  let text;
  if (v.kind === 'block') {
    const [r0, r1] = v.range;
    const nl = src.slice(r0, r1).endsWith('\n') ? '\n' : '';
    text = src.slice(0, r0) + formatted + nl + src.slice(r1);
  } else {
    const r0 = v.kind === 'null' ? E.insertAt : v.range[0];
    const r1 = v.kind === 'null' ? E.insertAt : v.range[1];
    const lead = v.kind === 'null' ? ' ' : '';
    if (!multi) {
      text = src.slice(0, r0) + lead + formatted + src.slice(r1);
    } else {
      // A block scalar's content goes on the following lines, so whatever trailed the old value on
      // its line (a comment) moves up to the header.
      let eol = src.indexOf('\n', r1);
      if (eol < 0) eol = src.length;
      const nl = formatted.indexOf('\n');
      text = src.slice(0, r0) + lead + formatted.slice(0, nl) + src.slice(r1, eol) + formatted.slice(nl) + src.slice(eol);
    }
  }
  const parsed = scalarValue(formatted, E.type === 'item' ? 'item' : 'value');
  return { text, expect: (js) => setAt(js, E.path, parsed) };
}

function opRename(doc, { id, key }, hooks) {
  const E = entryOf(doc, id);
  if (E.disabled || E.type !== 'pair' || E.readonly) throw new OpError('This name can only be changed in the YAML view.');
  const next = String(key ?? '').trim();
  if (next === E.key) return { text: doc.text, expect: SAME };
  if (!next) throw new OpError('The name can’t be empty.');
  const P = parentOf(doc, E);
  if (P.children.some((c) => c !== E && !c.disabled && c.key === next)) throw new OpError(`"${next}" already exists here.`);
  const bad = hooks.checkKey && hooks.checkKey(P, next, doc);
  if (bad) throw new OpError(bad);
  const text = doc.text.slice(0, E.keyRange[0]) + formatKey(next) + doc.text.slice(E.keyRange[1]);
  return {
    text,
    expect: (js) => {
      const obj = P.path.length ? getAt(js, P.path) : js;
      const val = obj[E.key];
      delete obj[E.key];
      obj[next] = val;
      return js;
    },
  };
}

function opAdd(doc, { parent, key, kind = 'scalar', value = '' }, hooks) {
  const P = entryOf(doc, parent);
  if (P.disabled || P.readonly) throw new OpError('Enable it before adding to it.');
  const v = P.value;
  let childType;
  if (P.type === 'root' || v.kind === 'map' || v.kind === 'flowmap') childType = 'pair';
  else if (v.kind === 'seq' || v.kind === 'flowseq') childType = 'item';
  else if (v.kind === 'null') childType = key != null ? 'pair' : 'item';
  else throw new OpError('Entries can only be added to a list or a mapping.');
  if ((v.kind === 'flowseq' || v.kind === 'flowmap') && !v.empty) throw new OpError('This is an inline list. Convert it to block style first.');
  if (childType === 'pair') {
    key = String(key ?? '').trim();
    if (!key) throw new OpError('Enter a name.');
    if (P.children.some((c) => !c.disabled && c.key === key)) throw new OpError(`"${key}" already exists here.`);
    const bad = hooks.checkKey && hooks.checkKey(P, key, doc);
    if (bad) throw new OpError(bad);
  }
  const lines = doc.lines.slice();
  const live = enabledChildren(P);
  const off = P.children.filter((c) => c.disabled);
  let col;
  if (live.length) col = live[0].col;
  else if (off.length) col = Math.min(...off.map((d) => d.L));
  else if (P.type === 'root') col = 0;
  else if (P.type === 'item') col = P.col + 2;
  else if (childType === 'item' && doc.seqStyle === 'indentless') col = P.col;
  else col = P.col + doc.indentUnit;

  if ((v.kind === 'flowseq' || v.kind === 'flowmap') && v.empty) {
    const [s, e] = v.range;
    const ln = doc.lineOf(s);
    let a = doc.colOf(s);
    while (a > 0 && lines[ln][a - 1] === ' ') a--;
    lines[ln] = lines[ln].slice(0, a) + lines[ln].slice(doc.colOf(e));
  }
  const inner = childType === 'item' ? col + 2 : col + doc.indentUnit;
  const allow = (hooks.allowTypesFor && hooks.allowTypesFor(P, key, doc)) || {};
  const valueText = kind === 'map' ? '{}' : kind === 'seq' ? '[]'
    : formatScalar(String(value ?? ''), { ctx: childType === 'item' ? 'item' : 'value', allow, indent: inner });
  const head = childType === 'pair' ? formatKey(key) + ': ' : '- ';
  const newLines = (' '.repeat(col) + head + valueText).split('\n');

  if (P.type === 'item' && v.kind === 'null' && /^\s*-\s*$/.test(lines[P.line])) {
    lines[P.line] = lines[P.line].trimEnd() + ' ' + newLines[0].trimStart();
    lines.splice(P.line + 1, 0, ...newLines.slice(1));
  } else {
    let after;
    if (P.type === 'root') {
      after = lines.length - 1;
      while (after >= 0 && lines[after] === '' && after === lines.length - 1) after--;
    } else {
      after = Math.max(P.line, P.spanEnd);
    }
    lines.splice(after + 1, 0, ...newLines);
  }
  if (lines[lines.length - 1] !== '') lines.push('');
  const parsed = kind === 'map' ? {} : kind === 'seq' ? [] : scalarValue(valueText, childType === 'item' ? 'item' : 'value');
  const where = childType === 'item' ? live.length : key;
  return { text: lines.join('\n'), expect: (js) => insertAt(js, P.path, where, parsed) };
}

function opRemove(doc, { id }) {
  const E = entryOf(doc, id);
  if (E.type === 'root') throw new OpError("Can't remove that.");
  const lines = doc.lines.slice();
  if (E.disabled) {
    lines.splice(E.line, E.endLine - E.line + 1);
    return { text: lines.join('\n'), expect: SAME };
  }
  const P = parentOf(doc, E);
  const live = enabledChildren(P);
  if (P.type === 'root' && live.length === 1) throw new OpError("Can't remove the last top-level entry: compose rejects a file with nothing in it.");
  checkAnchors(doc, E);
  let from = E.line, to = E.spanEnd;
  if (E.onDashLine) {
    splitDashLine(doc, lines, E);
    from++;
    to++;
  }
  lines.splice(from, to - from + 1);
  if (live.length === 1 && P.type !== 'root') {
    emptyPlaceholder(doc, lines, P);
  } else if (E.onDashLine) {
    // The next key now sits under a bare dash; put it back on the dash line.
    const next = lines[P.line + 1];
    if (next !== undefined && /^\s*-\s*$/.test(lines[P.line]) && next.length - next.trimStart().length === E.col && next.trim() !== '' && !next.trimStart().startsWith('#')) {
      lines[P.line] = lines[P.line].trimEnd() + ' ' + next.trimStart();
      lines.splice(P.line + 1, 1);
    }
  }
  return { text: lines.join('\n'), expect: (js) => deleteAt(js, E.path) };
}

// flowItems rewrites a one-line inline list or map (`["CMD", "curl"]`) from its items: unchanged
// items keep their original text, edited ones are formatted to match.
function opFlowItems(doc, { id, items }) {
  const E = entryOf(doc, id);
  const v = E.value;
  if (E.disabled || !v || !(v.kind === 'flowseq' || v.kind === 'flowmap') || !v.singleLine || (!v.items && !v.empty)) {
    throw new OpError('This value can only be edited in the YAML view.');
  }
  const style = v.items && v.items.length && v.items[0].style !== 'PLAIN' ? v.items[0].style : null;
  const val = (it) => (it.src != null && it.src !== '' ? it.src : formatScalar(String(it.value ?? ''), { ctx: 'flow', style }));
  const inner = v.kind === 'flowseq'
    ? '[' + items.map(val).join(', ') + ']'
    : '{' + items.map((it) => (it.keySrc != null ? it.keySrc : formatKey(it.key, 'flow')) + ': ' + val(it)).join(', ') + '}';
  const text = doc.text.slice(0, v.range[0]) + inner + doc.text.slice(v.range[1]);
  let parsed;
  try {
    const d = parseDocument('k: ' + inner + '\n');
    if (d.errors.length) throw new Error();
    parsed = d.toJS().k;
  } catch {
    throw new OpError(SAFE);
  }
  return { text, expect: (js) => setAt(js, E.path, parsed) };
}

// toBlock turns an inline list or map into one entry per line, so items can be added, disabled and
// removed individually.
function opToBlock(doc, { id }) {
  const E = entryOf(doc, id);
  const v = E.value;
  if (E.disabled || E.type !== 'pair' || !v || !(v.kind === 'flowseq' || v.kind === 'flowmap')) throw new OpError('Only inline lists can be converted.');
  const src = doc.text;
  const [s, e] = v.range;
  if (doc.lineOf(s) !== E.line) throw new OpError(SAFE);
  const d = parseDocument('k: ' + src.slice(s, e) + '\n');
  const flow = d.contents && d.contents.items[0] && d.contents.items[0].value;
  if (!flow || d.errors.length) throw new OpError(SAFE);
  const fsrc = 'k: ' + src.slice(s, e) + '\n';
  const piece = (n) => {
    const t = fsrc.slice(n.range[0], n.range[1]);
    if (t.includes('\n') || !isScalar(n)) throw new OpError('This inline list has nested values. Convert it in the YAML view.');
    return t;
  };
  const col = v.kind === 'flowseq' && doc.seqStyle === 'indentless' ? E.col : E.col + doc.indentUnit;
  const body = flow.items.map((it) => ' '.repeat(col) + (v.kind === 'flowseq' ? '- ' + piece(it) : piece(it.key) + ': ' + (it.value ? piece(it.value) : '')));
  const lastLine = doc.lineOf(e - 1);
  const lines = doc.lines.slice();
  const startCol = doc.colOf(s);
  const endCol = doc.colOf(e);
  const header = lines[E.line].slice(0, startCol).trimEnd() + lines[lastLine].slice(endCol);
  lines.splice(E.line, lastLine - E.line + 1, header, ...body);
  return { text: lines.join('\n'), expect: SAME };
}

const OPS = {
  set: opSet, rename: opRename, disable: opDisable, enable: opEnable, add: opAdd, remove: opRemove,
  flowItems: opFlowItems, toBlock: opToBlock,
};

// ---------------------------------------------------------------------------------------------
// scalar formatting

function plainOk(s, ctx, allow) {
  if (s === '' || s !== s.trim() || CTRL.test(s) || s.includes('\n')) return false;
  const wrap = ctx === 'item' ? `- ${s}\n` : ctx === 'flow' ? `[${s}]\n` : `k: ${s}\n`;
  for (const version of ['1.1', '1.2']) {
    let d;
    try {
      d = parseDocument(wrap, { version });
    } catch {
      return false;
    }
    if (d.errors.length || d.warnings.length) return false;
    const c = d.contents;
    let node;
    if (ctx === 'value') {
      if (!isMap(c) || c.items.length !== 1) return false;
      node = c.items[0].value;
    } else {
      if (!isSeq(c) || c.items.length !== 1) return false;
      node = c.items[0];
    }
    if (!isScalar(node) || node.type !== 'PLAIN' || wrap.slice(node.range[0], node.range[1]) !== s) return false;
    const val = node.value;
    if (typeof val === 'string') {
      if (val !== s) return false;
    } else if (typeof val === 'number') {
      if (!allow.number || String(val) !== s) return false;
    } else if (typeof val === 'boolean') {
      if (!allow.boolean || String(val) !== s) return false;
    } else return false;
  }
  return true;
}

function dq(s) {
  return JSON.stringify(s).replace(/[\u0085\u2028\u2029]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}

// formatScalar renders a string as YAML so that it reads back as exactly that string under both
// YAML 1.1 (what docker compose's parser follows for `yes`, `0755`, `22:22`) and 1.2. It stays
// plain when that is unambiguous, keeps an existing quote style, and uses a literal block for
// multi-line text. allow.number / allow.boolean let a numeric or boolean field stay unquoted.
export function formatScalar(s, { ctx = 'value', style = null, allow = {}, indent = 2 } = {}) {
  s = String(s);
  if (s.includes('\n')) {
    if (ctx === 'flow' || /^\n*[ \t]/.test(s) || /[ \t]$/m.test(s) || CTRL.test(s) || /\r/.test(s)) return dq(s);
    const trailing = /\n*$/.exec(s)[0].length;
    const chomp = trailing === 0 ? '-' : trailing === 1 ? '' : '+';
    const body = s.slice(0, s.length - trailing);
    const pad = ' '.repeat(Math.max(1, indent));
    return '|' + chomp + '\n' + body.split('\n').map((l) => (l === '' ? '' : pad + l)).join('\n') + '\n'.repeat(Math.max(0, trailing - 1));
  }
  if (style === 'QUOTE_SINGLE' && !CTRL.test(s)) return "'" + s.replace(/'/g, "''") + "'";
  if (style === 'QUOTE_DOUBLE') return dq(s);
  if (plainOk(s, ctx, allow)) return s;
  if (/["\\]/.test(s) && !s.includes("'") && !CTRL.test(s)) return "'" + s + "'";
  return dq(s);
}

// formatKey renders a mapping key, quoting it unless plain text reads back identically.
export function formatKey(k, ctx = 'block') {
  k = String(k);
  if (k !== '' && k === k.trim() && !CTRL.test(k) && !k.includes('\n')) {
    let ok = true;
    for (const version of ['1.1', '1.2']) {
      const src = ctx === 'flow' ? `{${k}: x}\n` : `${k}: x\n`;
      const d = parseDocument(src, { version });
      const c = d.contents;
      const p = isMap(c) && c.items.length === 1 ? c.items[0] : null;
      if (d.errors.length || d.warnings.length || !p || !isScalar(p.key) || p.key.type !== 'PLAIN' || p.key.value !== k || !isScalar(p.value) || p.value.value !== 'x') {
        ok = false;
        break;
      }
    }
    if (ok) return k;
  }
  return dq(k);
}

// scalarValue is the data a formatted scalar reads back as, in the parser load() uses.
function scalarValue(formatted, ctx) {
  const src = ctx === 'item' ? `- ${formatted}\n` : `k: ${formatted}\n`;
  const d = parseDocument(src);
  if (d.errors.length) throw new OpError(SAFE);
  const js = d.toJS();
  return ctx === 'item' ? js[0] : js.k;
}

// ---------------------------------------------------------------------------------------------
// data helpers

function getAt(js, path) {
  let v = js;
  for (const p of path) {
    if (v == null) return undefined;
    v = v[p];
  }
  return v;
}

function setAt(js, path, value) {
  if (!path.length) return value;
  getAt(js, path.slice(0, -1))[path[path.length - 1]] = value;
  return js;
}

function deleteAt(js, path) {
  const parent = getAt(js, path.slice(0, -1));
  const k = path[path.length - 1];
  if (Array.isArray(parent)) parent.splice(k, 1);
  else delete parent[k];
  return js;
}

function insertAt(js, path, where, value) {
  let container = path.length ? getAt(js, path) : js;
  if (container == null) {
    container = typeof where === 'number' ? [] : {};
    if (path.length) setAt(js, path, container);
    else js = container;
  }
  if (Array.isArray(container)) container.splice(where, 0, value);
  else container[where] = value;
  return js;
}

// canon serializes data with object keys sorted: the post-condition compares data, not key order.
export function canon(v) {
  if (Array.isArray(v)) return '[' + v.map(canon).join(',') + ']';
  if (v && typeof v === 'object') return '{' + Object.keys(v).sort().map((k) => JSON.stringify(k) + ':' + canon(v[k])).join(',') + '}';
  return JSON.stringify(v === undefined ? null : v);
}

// ---------------------------------------------------------------------------------------------
// references and lint

const listOrKeys = (v) => (Array.isArray(v) ? v.map(String) : v && typeof v === 'object' ? Object.keys(v) : []);

// lint returns data-level problems compose will reject (or warn about) when the file is saved.
export function lint(js) {
  const warnings = [];
  if (!js || typeof js !== 'object') return warnings;
  if ('version' in js) warnings.push('`version` is obsolete; docker compose ignores it.');
  const services = js.services && typeof js.services === 'object' ? js.services : {};
  const names = (section) => new Set(Object.keys((js[section] && typeof js[section] === 'object' && js[section]) || {}));
  const svcNames = new Set(Object.keys(services));
  for (const [svc, def] of Object.entries(services)) {
    if (!def || typeof def !== 'object') {
      warnings.push(`Service "${svc}" is empty; compose needs at least an image or a build.`);
      continue;
    }
    if (!def.image && !def.build && !def.extends) warnings.push(`Service "${svc}" has neither an image nor a build.`);
    for (const dep of listOrKeys(def.depends_on)) {
      if (!svcNames.has(dep)) warnings.push(`Service "${svc}" depends on "${dep}", which isn't an active service. Save will fail.`);
    }
    const volumes = names('volumes');
    for (const v of Array.isArray(def.volumes) ? def.volumes : []) {
      // Short syntax `name:/path`: a source that isn't a path is a named volume.
      const source = typeof v === 'string' ? (v.includes(':') ? v.split(':')[0] : null) : v && v.type === 'volume' ? v.source : null;
      if (source && !/^[./~$]/.test(source) && !volumes.has(source)) {
        warnings.push(`Service "${svc}" uses volume "${source}", which isn't defined at the top level. Save will fail.`);
      }
    }
    for (const [section, via] of [['networks', 'networks'], ['secrets', 'secrets'], ['configs', 'configs']]) {
      const have = names(section);
      const used = section === 'networks' ? listOrKeys(def.networks) : (Array.isArray(def[section]) ? def[section] : []).map((s) => (typeof s === 'string' ? s : s && s.source));
      for (const u of used) {
        if (u && !have.has(u) && !(section === 'networks' && u === 'default')) warnings.push(`Service "${svc}" uses ${via.slice(0, -1)} "${u}", which isn't defined at the top level. Save will fail.`);
      }
    }
  }
  return warnings;
}
