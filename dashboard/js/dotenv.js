// .env model for the stack's env file editor. Like the compose editor it works on lines: every line
// keeps its exact text until it is edited, so saving an untouched file is byte-identical and an
// edit changes only the lines it touches.
//
// Parsing follows docker compose's own dotenv reader (compose-go): optional `export `, `KEY=value`
// or `KEY: value`, single quotes literal, double quotes with escapes (both may span lines), ` #`
// starting a comment in an unquoted value, and a bare `KEY` inheriting from the environment. A
// comment that is itself a `KEY=value` line is a disabled variable. Pure; tested with node --test.

const KEY = /^[\p{L}\p{N}_.\-[\]]+$/u;
const NEW_KEY = /^[A-Za-z_][A-Za-z0-9_.-]*$/;
const SECRET = /(^|_)(PASS(WORD|WD)?|SECRETS?|TOKENS?|API_?KEY|KEYS?|PRIVATE|CREDENTIALS?|AUTH)(_|$)/i;
// Decorated comment lines: `# ── Database ──`, `# ------`, `#### app`.
const RULER = /^#+\s*[-=─━═*#~_]{2,}(?:\s+(.*?))?\s*[-=─━═*#~_]*\s*$|^#{2,}\s+(\S.*?)\s*#*\s*$/;

const PROSE = /^(NOTE|NOTES|NB|TODO|FIXME|WARN|WARNING|IMPORTANT|INFO|TIP|HINT|USAGE|EXAMPLE|EXAMPLES|SEE|DEFAULT|DEFAULTS|OPTIONAL|REQUIRED|DEPRECATED)$/;

export const isSecretKey = (key) => SECRET.test(key);

// scanVar reads a variable statement starting at lines[i]. It returns null when the line isn't one.
function scanVar(lines, i) {
  const line = lines[i];
  const m = /^(\s*)(export\s+)?([^\s=:#]+)(\s*)([=:])(\s*)(.*)$/u.exec(line);
  if (!m) {
    const bare = /^(\s*)(export\s+)?([^\s=:#]+)\s*$/u.exec(line);
    if (bare && KEY.test(bare[3])) return { t: 'bare', start: i, end: i, key: bare[3], exported: !!bare[2] };
    return null;
  }
  const [, indent, exp, key, sp1, sep, sp2, rest] = m;
  if (!KEY.test(key)) return null;
  const head = indent + (exp || '') + key + sp1 + sep + sp2;
  const q = rest[0];
  if (q === '"' || q === "'") {
    // Scan for the closing quote, across lines; a backslash escapes the next character.
    let text = rest;
    let j = i;
    for (;;) {
      let esc = false;
      for (let k = 1; k < text.length; k++) {
        const c = text[k];
        if (esc) {
          esc = false;
          continue;
        }
        if (c === '\\') {
          esc = true;
          continue;
        }
        if (c === q) {
          const inner = text.slice(1, k);
          return {
            t: 'var', start: i, end: j, key, exported: !!exp, sep, head, quote: q,
            value: inner, trail: text.slice(k + 1), multiline: j > i,
          };
        }
      }
      if (j + 1 >= lines.length) return { t: 'invalid', start: i, end: j, key, error: `unterminated quoted value for ${key}` };
      j++;
      text += '\n' + lines[j];
    }
  }
  const cut = rest.indexOf(' #');
  const valuePart = cut >= 0 ? rest.slice(0, cut) : rest;
  const value = valuePart.replace(/\s+$/, '');
  return {
    t: 'var', start: i, end: i, key, exported: !!exp, sep, head, quote: '',
    value, trail: rest.slice(value.length), multiline: false,
  };
}

// parseEnv splits text into statements. Every statement records the line range it covers.
export function parseEnv(text) {
  const src = String(text ?? '').replace(/\r\n?/g, '\n');
  const lines = src.split('\n');
  const items = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim() === '') {
      items.push({ t: 'blank', start: i, end: i });
      continue;
    }
    const cm = /^(\s*#\s?)(.*)$/.exec(line);
    if (cm) {
      // A comment whose body is a single-line `KEY=value` is a disabled variable. The `KEY: value`
      // form only counts for an ENV_STYLE key, so prose like `# Note: something` or `# TODO: x`
      // stays a comment.
      const body = cm[2];
      const looksVar = /^(export\s+)?[A-Za-z_][A-Za-z0-9_.-]*\s*=/.test(body)
        || (/^(export\s+)?[A-Z_][A-Z0-9_]*:\s/.test(body) && !PROSE.test(body.replace(/^export\s+/, '').split(':')[0]));
      const inner = looksVar ? scanVar([body], 0) : null;
      if (inner && inner.t === 'var' && !inner.multiline) {
        items.push({ ...inner, t: 'disabled', start: i, end: i, hash: cm[1] });
        continue;
      }
      const ruler = RULER.exec(line.trim());
      items.push({ t: 'comment', start: i, end: i, text: body, section: ruler ? (ruler[1] || ruler[2] || '').trim() : null, ruler: !!ruler });
      continue;
    }
    const v = scanVar(lines, i);
    if (!v) {
      items.push({ t: 'invalid', start: i, end: i, error: /\s/.test(line.trim().split(/[=:]/)[0]) ? 'key cannot contain a space' : 'not a KEY=value line' });
      continue;
    }
    items.push(v);
    i = v.end;
  }
  // Later definitions win in compose; mark the ones they shadow.
  const seen = new Set();
  for (let k = items.length - 1; k >= 0; k--) {
    const it = items[k];
    if (it.t !== 'var' && it.t !== 'bare') continue;
    it.shadowed = seen.has(it.key);
    seen.add(it.key);
  }
  return { lines, items };
}

// envRows turns a parsed file into the rows the editor shows: sections, variables with the comment
// lines directly above them as help, disabled variables, and lines compose would reject.
export function envRows(model) {
  const rows = [];
  let help = [];
  for (const it of model.items) {
    if (it.t === 'comment') {
      if (it.section) {
        rows.push({ t: 'section', id: 'l' + it.start, text: it.section });
        help = [];
      } else if (!it.ruler) help.push(it.text.trim());
      continue;
    }
    if (it.t === 'blank') {
      help = [];
      continue;
    }
    const base = { id: 'l' + it.start, line: it.start, key: it.key || '', help: help.join(' ') };
    help = [];
    if (it.t === 'var') {
      rows.push({ ...base, t: 'var', value: it.value, quote: it.quote, multiline: it.multiline, secret: isSecretKey(it.key), shadowed: it.shadowed });
    } else if (it.t === 'disabled') {
      rows.push({ ...base, t: 'disabled', value: it.value, quote: it.quote, secret: isSecretKey(it.key) });
    } else if (it.t === 'bare') {
      rows.push({ ...base, t: 'bare', shadowed: it.shadowed });
    } else {
      rows.push({ ...base, t: 'invalid', text: model.lines.slice(it.start, it.end + 1).join('\n'), error: it.error });
    }
  }
  return rows;
}

// quoteValue writes a value so compose reads back exactly that string. It stays unquoted when that
// is safe; otherwise single quotes (literal), or double quotes when the value uses $ interpolation,
// contains a single quote or ends in a backslash.
export function quoteValue(v) {
  v = String(v ?? '');
  const needs = v !== v.trim() || v.includes(' #') || /^["'#]/.test(v) || /[\n\r]/.test(v) || v.startsWith(' ');
  if (!needs) return v;
  if (!v.includes('$') && !v.includes("'") && !v.endsWith('\\') && !/[\n\r]/.test(v)) return "'" + v + "'";
  return '"' + v.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r') + '"';
}

// escapeIn escapes unescaped quote characters typed into an already-quoted value.
function escapeIn(v, q) {
  let out = '';
  for (let i = 0; i < v.length; i++) {
    const c = v[i];
    if (c === '\\') {
      out += c + (v[i + 1] ?? '');
      i++;
    } else if (c === q) out += '\\' + c;
    else if (c === '\n') out += q === '"' ? '\\n' : c;
    else out += c;
  }
  return out;
}

export class EnvError extends Error {}

function itemAt(model, line) {
  const it = model.items.find((x) => x.start === line && x.t !== 'blank' && x.t !== 'comment');
  if (!it) throw new EnvError('That line changed — try again.');
  return it;
}

function varText(it, value) {
  const q = it.quote;
  const body = q ? q + escapeIn(value, q) + q : quoteValue(value);
  // An unquoted value's inline comment (` # …`) stays after the new value.
  return it.head + body + (q ? it.trail : it.trail.replace(/^\s*/, (s) => (it.trail.trim() ? s || ' ' : '')));
}

function replaceLines(model, it, newLines) {
  const lines = model.lines.slice();
  lines.splice(it.start, it.end - it.start + 1, ...newLines);
  return lines.join('\n');
}

// Edits return the new text. The line numbers they take come from the model they are applied to.
export const envOps = {
  setValue(model, line, value) {
    const it = itemAt(model, line);
    if (it.t !== 'var' && it.t !== 'disabled') throw new EnvError('Only variables have values.');
    if (it.multiline) throw new EnvError('This value spans several lines. Edit it in the raw view.');
    const text = varText(it, value);
    return replaceLines(model, it, [(it.t === 'disabled' ? it.hash : '') + text]);
  },
  setKey(model, line, key) {
    const it = itemAt(model, line);
    key = String(key ?? '').trim();
    if (!NEW_KEY.test(key)) throw new EnvError('Names use letters, digits, _ . and -, and don\'t start with a digit.');
    if (it.t !== 'var' && it.t !== 'disabled' && it.t !== 'bare') throw new EnvError('Only variables have names.');
    const lines = model.lines.slice();
    const first = model.lines[it.start];
    const at = first.indexOf(it.key, it.t === 'disabled' ? it.hash.length : 0);
    lines[it.start] = first.slice(0, at) + key + first.slice(at + it.key.length);
    return lines.join('\n');
  },
  toggle(model, line) {
    const it = itemAt(model, line);
    if (it.t === 'disabled') return replaceLines(model, it, [model.lines[it.start].slice(it.hash.length)]);
    // A bare `KEY` commented out would read as prose, with no way back from this view.
    if (it.t !== 'var') throw new EnvError('Only KEY=value variables can be disabled.');
    if (it.multiline) throw new EnvError('This value spans several lines. Comment it out in the raw view.');
    return replaceLines(model, it, ['# ' + model.lines[it.start]]);
  },
  remove(model, line) {
    const it = itemAt(model, line);
    return replaceLines(model, it, []);
  },
  add(model, key, value) {
    key = String(key ?? '').trim();
    if (!NEW_KEY.test(key)) throw new EnvError('Names use letters, digits, _ . and -, and don\'t start with a digit.');
    const lines = model.lines.slice();
    while (lines.length > 1 && lines[lines.length - 1] === '' && lines[lines.length - 2] === '') lines.pop();
    const newLine = key + '=' + quoteValue(value);
    if (lines.length === 1 && lines[0] === '') return newLine + '\n';
    if (lines[lines.length - 1] === '') lines.splice(lines.length - 1, 0, newLine);
    else lines.push(newLine, '');
    return lines.join('\n');
  },
};

// readValue is the value compose would read for a parsed variable (escapes applied, no
// interpolation), for checking that written values read back unchanged.
export function readValue(it) {
  if (it.quote === "'") return it.value.replace(/\\'/g, "'");
  if (it.quote === '"') {
    return it.value.replace(/\\(["\\$abfnrtv])/g, (_, c) => ({ n: '\n', r: '\r', t: '\t', a: '\x07', b: '\b', f: '\f', v: '\v' }[c] ?? (c === '$' ? '$$' : c)));
  }
  return it.value;
}
