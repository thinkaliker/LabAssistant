// ANSI SGR escapes → HTML for the log views (job panel, container logs, credential prompt).
// Text is HTML-escaped first; only styled runs are wrapped in spans, so output is safe for x-html.
// Handles bold/dim/italic/underline, the 16 base colors (themed via --ansi-N in stylesheet.css),
// 256-color, and truecolor. Every other escape (cursor moves, erase, OSC titles) is dropped.

// CSI (ESC [ params intermediates final), OSC (ESC ] … BEL|ST), or a bare two-byte escape.
const SEQ = /\x1b\[([0-?]*)[ -/]*([@-~])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-_]?/g;

const HTML_ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const escapeHtml = (s) => s.replace(/[&<>"']/g, (c) => HTML_ESC[c]);

// color256 maps an xterm 256-color index to a CSS color: the first 16 use the themed palette,
// then the 6×6×6 cube, then the 24-step gray ramp.
function color256(n) {
  if (n < 16) return `var(--ansi-${n})`;
  if (n < 232) {
    n -= 16;
    const v = (x) => (x ? x * 40 + 55 : 0);
    return `rgb(${v(Math.floor(n / 36))},${v(Math.floor(n / 6) % 6)},${v(n % 6)})`;
  }
  const g = (n - 232) * 10 + 8;
  return `rgb(${g},${g},${g})`;
}

function applySGR(st, params) {
  const p = params === '' ? [0] : params.split(';').map((x) => parseInt(x, 10) || 0);
  for (let i = 0; i < p.length; i++) {
    const c = p[i];
    if (c === 0) { st.bold = st.dim = st.italic = st.underline = false; st.fg = st.bg = null; }
    else if (c === 1) st.bold = true;
    else if (c === 2) st.dim = true;
    else if (c === 3) st.italic = true;
    else if (c === 4) st.underline = true;
    else if (c === 22) st.bold = st.dim = false;
    else if (c === 23) st.italic = false;
    else if (c === 24) st.underline = false;
    else if (c >= 30 && c <= 37) st.fg = `var(--ansi-${c - 30})`;
    else if (c >= 90 && c <= 97) st.fg = `var(--ansi-${c - 90 + 8})`;
    else if (c === 39) st.fg = null;
    else if (c >= 40 && c <= 47) st.bg = `var(--ansi-${c - 40})`;
    else if (c >= 100 && c <= 107) st.bg = `var(--ansi-${c - 100 + 8})`;
    else if (c === 49) st.bg = null;
    else if (c === 38 || c === 48) {
      let color = null;
      if (p[i + 1] === 5) { color = color256(Math.min(p[i + 2] || 0, 255)); i += 2; }
      else if (p[i + 1] === 2) {
        const [r, g, b] = [p[i + 2], p[i + 3], p[i + 4]].map((x) => Math.min(x || 0, 255));
        color = `rgb(${r},${g},${b})`; i += 4;
      }
      if (c === 38) st.fg = color; else st.bg = color;
    }
  }
}

// openTag renders the current style as a <span>, or '' when unstyled. Style values are built
// from parsed integers only, never from the input text.
function openTag(st) {
  const cls = [];
  if (st.bold) cls.push('ansi-bold');
  if (st.dim) cls.push('ansi-dim');
  if (st.italic) cls.push('ansi-italic');
  if (st.underline) cls.push('ansi-underline');
  const css = [];
  if (st.fg) css.push(`color:${st.fg}`);
  if (st.bg) css.push(`background-color:${st.bg}`);
  if (!cls.length && !css.length) return '';
  return '<span' + (cls.length ? ` class="${cls.join(' ')}"` : '') + (css.length ? ` style="${css.join(';')}"` : '') + '>';
}

export function ansiToHtml(text) {
  if (!text) return '';
  if (!text.includes('\x1b')) return escapeHtml(text);
  const st = { bold: false, dim: false, italic: false, underline: false, fg: null, bg: null };
  let out = '', tag = '', last = 0;
  const emit = (chunk) => { if (chunk) out += tag ? tag + escapeHtml(chunk) + '</span>' : escapeHtml(chunk); };
  for (const m of text.matchAll(SEQ)) {
    emit(text.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] === 'm') { applySGR(st, m[1]); tag = openTag(st); }
  }
  emit(text.slice(last));
  return out;
}

export const ansi = {
  ansiHtml: ansiToHtml,
};
