import test from 'node:test';
import assert from 'node:assert/strict';
import { commentEdits, applyEdits, lineRange, mapOffset, isCommentShortcut } from '../js/linecomment.js';
import { load, apply } from '../js/composedoc.js';
import { S, id } from './helpers.mjs';

// toggle runs commentEdits over text and returns the new text.
const toggle = (text, ranges) => {
  const lines = text.split('\n');
  return applyEdits(lines, commentEdits(lines, ranges)).join('\n');
};

const compose = [
  'services:',
  '  web:',
  '    image: nginx',
  '    ports:',
  '      - "80:80"',
  '',
  '  redis:',
  '    image: redis:7',
  '    command: redis-server',
].join('\n');

test('comments a block at its smallest indent, leaving blank lines alone', () => {
  assert.equal(toggle(compose, [[3, 8]]), [
    'services:',
    '  web:',
    '    image: nginx',
    '  #   ports:',
    '  #     - "80:80"',
    '',
    '  # redis:',
    '  #   image: redis:7',
    '  #   command: redis-server',
  ].join('\n'));
  assert.equal(toggle(compose, [[3, 4]]), compose.replace('    ports:\n      - "80:80"', '    # ports:\n    #   - "80:80"'));
});

test('uncomments when every non-blank line is a comment, one level at a time', () => {
  const text = '# A=1\n#B=2\n\n  # # C=3';
  assert.equal(toggle(text, [[0, 3]]), 'A=1\nB=2\n\n  # C=3');
});

test('a mix of comments and code is commented as a whole', () => {
  assert.equal(toggle('A=1\n# B=2', [[0, 1]]), '# A=1\n# # B=2');
});

test('toggling twice gives back the original', () => {
  for (const [a, b] of [[0, 8], [1, 4], [6, 8], [5, 5], [2, 2]]) {
    assert.equal(toggle(toggle(compose, [[a, b]]), [[a, b]]), compose, `lines ${a}-${b}`);
  }
});

test('a cursor on a blank line starts a comment there', () => {
  assert.equal(toggle('A=1\n\nB=2', [[1, 1]]), 'A=1\n# \nB=2');
});

test('several cursors: commented or not is decided together, indented per block', () => {
  assert.equal(toggle(compose, [[2, 2], [7, 8]]).split('\n').filter((l) => l.includes('#')).join('\n'),
    '    # image: nginx\n    # image: redis:7\n    # command: redis-server');
  // Overlapping selections touch each line once.
  assert.equal(toggle('A=1\nB=2', [[0, 1], [1, 1]]), '# A=1\n# B=2');
});

test('a selection ending at the start of a line leaves that line out', () => {
  assert.deepEqual(lineRange({ line: 2, ch: 0 }, { line: 5, ch: 0 }), [2, 4]);
  assert.deepEqual(lineRange({ line: 2, ch: 3 }, { line: 5, ch: 1 }), [2, 5]);
  assert.deepEqual(lineRange({ line: 2, ch: 0 }, { line: 2, ch: 0 }), [2, 2]);
});

test('offsets follow the edits', () => {
  const edits = [{ at: 4, remove: 0, insert: '# ' }, { at: 10, remove: 2, insert: '' }];
  assert.equal(mapOffset(0, edits), 0);
  assert.equal(mapOffset(4, edits), 6); // at an insertion point: after the new `# `
  assert.equal(mapOffset(7, edits), 9);
  assert.equal(mapOffset(11, edits), 12); // inside removed text: where it was
  assert.equal(mapOffset(15, edits), 15);
});

test('the shortcut is Cmd or Ctrl with /, by the character typed', () => {
  assert.ok(isCommentShortcut({ metaKey: true, ctrlKey: false, altKey: false, key: '/' }));
  assert.ok(isCommentShortcut({ metaKey: false, ctrlKey: true, altKey: false, key: '/' }));
  assert.ok(!isCommentShortcut({ metaKey: false, ctrlKey: false, altKey: false, key: '/' }));
  assert.ok(!isCommentShortcut({ metaKey: true, ctrlKey: false, altKey: true, key: '/' }));
  assert.ok(!isCommentShortcut({ metaKey: false, ctrlKey: true, altKey: false, key: '-' }));
});

// The compose Simple view and this toggle write the same shape, so each reads the other's.
test('a block commented in the YAML view is a disabled entry in the Simple view', () => {
  const doc = load(toggle(compose, [[6, 8]]), S.hooks);
  assert.deepEqual(doc.disabled.map((d) => d.key), ['redis']);
  assert.equal(doc.disabled[0].parent, id(['services']));
});

test('a block the Simple view disabled uncomments back to the original', () => {
  const r = apply(load(compose, S.hooks), { op: 'disable', id: id(['services', 'redis']) }, S.hooks);
  assert.ok(r.ok, r.error);
  const lines = r.text.split('\n');
  const first = lines.findIndex((l) => l.includes('# redis:'));
  assert.equal(toggle(r.text, [[first, lines.length - 1]]), compose);
});
