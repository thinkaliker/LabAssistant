// Toggle `#` line comments over the selected lines: Cmd/Ctrl+/ and the "Toggle comment" button in
// the compose editor's YAML view and the .env editor's Raw view (YAML and .env share the syntax).
//
// As in most code editors: when every non-blank line the selection touches is already a comment,
// one level of `#` comes off each; otherwise `# ` goes in front of each, at the smallest indent in
// the block, and blank lines are left alone. That is the shape the compose Simple view writes when
// it disables an entry, so a block commented here shows up there as "commented out", and a block
// disabled there uncomments here.
//
// commentEdits and the helpers under it are pure (tested in jstest/linecomment.test.mjs); the
// functions at the bottom apply them to CodeMirror and to a <textarea>.

const filled = (line) => /\S/.test(line);
const isComment = (line) => /^\s*#/.test(line);
const indentOf = (line) => /^\s*/.exec(line)[0].length;

// lineRange turns a selection (from <= to, each { line, ch }) into the lines it covers. A selection
// that ends at the very start of a line doesn't include that line: selecting whole lines by
// dragging (or Shift+Down) ends there.
export function lineRange(from, to) {
  const last = to.ch === 0 && to.line > from.line ? to.line - 1 : to.line;
  return [from.line, last];
}

// commentEdits returns the edits that toggle the comments on the given line ranges ([first, last],
// inclusive; several for multiple cursors): { line, ch, remove, insert }, one per changed line, in
// line order. Whether it comments or uncomments is decided over all the ranges together; when
// commenting, each range is indented on its own.
export function commentEdits(lines, ranges) {
  const merged = [];
  for (const [a, b] of [...ranges].sort((x, y) => x[0] - y[0])) {
    const prev = merged[merged.length - 1];
    if (prev && a <= prev[1]) prev[1] = Math.max(prev[1], b);
    else merged.push([a, b]);
  }
  const blocks = merged.map(([a, b]) => {
    const all = [];
    for (let i = a; i <= b; i++) all.push(i);
    return { all, text: all.filter((i) => filled(lines[i])) };
  });
  const text = blocks.flatMap((b) => b.text);

  if (text.length && text.every((i) => isComment(lines[i]))) {
    return text.map((i) => {
      const ch = lines[i].indexOf('#');
      return { line: i, ch, remove: lines[i][ch + 1] === ' ' ? 2 : 1, insert: '' };
    });
  }
  // Nothing but blank lines selected: comment those, so a lone cursor on an empty line still
  // starts a comment there.
  const pick = (b) => (text.length ? b.text : b.all);
  return blocks.flatMap((b) => {
    const target = pick(b);
    if (!target.length) return [];
    const ch = Math.min(...target.map((i) => indentOf(lines[i])));
    return target.map((i) => ({ line: i, ch, remove: 0, insert: '# ' }));
  });
}

// applyEdits returns the lines with the edits made.
export function applyEdits(lines, edits) {
  const out = lines.slice();
  for (const e of edits) out[e.line] = out[e.line].slice(0, e.ch) + e.insert + out[e.line].slice(e.ch + e.remove);
  return out;
}

// mapOffset moves a character offset in the old text to where it lands in the new one. edits carry
// absolute offsets ({ at, remove, insert }) in ascending order. A position at an insertion point
// moves past what was inserted (so the caret at a line's start ends up after its new `# `), and
// one inside removed text moves to where it was.
export function mapOffset(pos, edits) {
  let out = pos;
  for (const e of edits) {
    if (pos < e.at) break;
    if (pos >= e.at + e.remove) out += e.insert.length - e.remove;
    else out -= pos - e.at;
  }
  return out;
}

// isCommentShortcut matches Cmd+/ (macOS) or Ctrl+/ by the character typed, not the physical key,
// so it follows the keyboard layout (on a German layout / is Shift+7).
export function isCommentShortcut(e) {
  return (e.metaKey || e.ctrlKey) && !e.altKey && e.key === '/';
}

// The buttons' tooltip, naming the shortcut the way this platform writes it.
export function commentTitle() {
  const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent);
  return `Comment or uncomment the selected lines (${mac ? '⌘/' : 'Ctrl+/'})`;
}

// ---- applying it ----

const cmpPos = (a, b) => a.line - b.line || a.ch - b.ch;

// toggleCodeMirror toggles the lines under every selection of a CodeMirror 5 editor, as one undo
// step. CodeMirror moves the selections along with the edits itself.
export function toggleCodeMirror(cm) {
  if (cm.getOption('readOnly')) return;
  const ranges = cm.listSelections().map(({ anchor, head }) =>
    cmpPos(anchor, head) <= 0 ? lineRange(anchor, head) : lineRange(head, anchor));
  const edits = commentEdits(cm.getValue().split('\n'), ranges);
  // One operation is one undo step. The origin has no leading '+', so toggling twice in a row
  // stays two steps rather than being merged like typing.
  cm.operation(() => {
    for (const e of edits) cm.replaceRange(e.insert, { line: e.line, ch: e.ch }, { line: e.line, ch: e.ch + e.remove }, 'comment');
  });
}

// toggleTextarea toggles the lines under a <textarea>'s selection.
export function toggleTextarea(ta) {
  if (ta.readOnly || ta.disabled) return;
  const lines = ta.value.split('\n');
  const starts = [];
  let off = 0;
  for (const l of lines) { starts.push(off); off += l.length + 1; }
  const at = (pos) => {
    let i = 0;
    while (i + 1 < starts.length && starts[i + 1] <= pos) i++;
    return { line: i, ch: pos - starts[i] };
  };
  const { selectionStart: s, selectionEnd: end, selectionDirection: dir } = ta;
  const [first, last] = lineRange(at(s), at(end));
  const edits = commentEdits(lines, [[first, last]]);
  if (!edits.length) return;
  const from = starts[first];
  const to = starts[last] + lines[last].length;
  const text = applyEdits(lines, edits).slice(first, last + 1).join('\n');
  ta.focus();
  ta.setSelectionRange(from, to);
  // insertText keeps the change on the textarea's own undo stack (Cmd/Ctrl+Z), which setting
  // .value would wipe, and fires 'input' so x-model sees it. setRangeText is the fallback.
  if (!document.execCommand('insertText', false, text)) {
    ta.setRangeText(text, from, to);
    ta.dispatchEvent(new Event('input', { bubbles: true }));
  }
  const abs = edits.map((e) => ({ at: starts[e.line] + e.ch, remove: e.remove, insert: e.insert }));
  ta.setSelectionRange(mapOffset(s, abs), mapOffset(end, abs), dir);
}
