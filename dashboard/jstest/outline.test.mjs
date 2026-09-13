import test from 'node:test';
import assert from 'node:assert/strict';
import { load, normalize } from '../js/composedoc.js';
import { S, fixture, fixtureNames, id } from './helpers.mjs';

test('every fixture loads', () => {
  for (const name of fixtureNames) {
    const doc = load(fixture(name), S.hooks);
    assert.equal(doc.blocked, '', `${name} blocked: ${doc.blocked}`);
  }
});

test('entries carry path, column, lines and kind', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const pick = (p) => {
    const e = doc.byId.get(id(p));
    assert.ok(e, `missing ${JSON.stringify(p)}`);
    return { col: e.col, line: e.line, endLine: e.endLine, kind: e.value.kind };
  };
  assert.deepEqual(pick(['name']), { col: 0, line: 0, endLine: 0, kind: 'scalar' });
  assert.deepEqual(pick(['services', 'web']), { col: 2, line: 2, endLine: 21, kind: 'map' });
  assert.deepEqual(pick(['services', 'web', 'ports']), { col: 4, line: 5, endLine: 6, kind: 'seq' });
  // A list item's column is its dash, not its value.
  assert.deepEqual(pick(['services', 'web', 'ports', 0]), { col: 6, line: 6, endLine: 6, kind: 'scalar' });
  assert.deepEqual(pick(['services', 'web', 'volumes', 0]), { col: 6, line: 12, endLine: 14, kind: 'map' });
  assert.deepEqual(pick(['services', 'web', 'healthcheck', 'test']), { col: 6, line: 17, endLine: 17, kind: 'flowseq' });
  assert.deepEqual(pick(['services', 'web', 'labels']), { col: 4, line: 19, endLine: 19, kind: 'null' });

  const type = doc.byId.get(id(['services', 'web', 'volumes', 0, 'type']));
  assert.equal(type.onDashLine, true);
  assert.equal(doc.byId.get(id(['services', 'web', 'volumes', 0, 'source'])).onDashLine, false);
  // The disabled port below the live one extends the list's span.
  assert.equal(doc.byId.get(id(['services', 'web', 'ports'])).spanEnd, 7);
});

test('plain scalars display their source; quoted and block scalars their value', () => {
  const doc = load(fixture('scalars.yaml'), S.hooks);
  const v = (k) => doc.byId.get(id(['services', 'scalars', 'labels', k])).value;
  assert.equal(v('octal').display, '0755');
  assert.equal(v('version').display, '3.10');
  assert.equal(v('port').display, '80:80');
  assert.equal(v('single').display, "it's");
  assert.equal(v('keep.newlines').kind, 'block');
  assert.equal(v('keep.newlines').display, 'trailing\n\n');
  assert.equal(v('quoted.multi').singleLine, false);
  assert.equal(v('tilde').kind, 'scalar');
});

test('layout detection', () => {
  assert.equal(load(fixture('indent4.yaml')).indentUnit, 4);
  assert.equal(load(fixture('basic.yaml')).indentUnit, 2);
  assert.equal(load(fixture('indentless.yaml')).seqStyle, 'indentless');
  assert.equal(load(fixture('basic.yaml')).seqStyle, 'indented');
});

test('read-only entries', () => {
  const doc = load(fixture('anchors.yaml'), S.hooks);
  assert.equal(doc.byId.get(id(['services', 'one', '<<'])).readonly, 'YAML merge key');
  assert.equal(doc.byId.get(id(['x-common', 'logging'])).readonly, 'YAML alias');
  assert.equal(doc.byId.get(id(['services', 'one', 'ports'])).readonly, null); // `!reset []` is fine
  const flow = load(fixture('flow.yaml'), S.hooks);
  assert.equal(flow.byId.get(id(['services', 'flow', 'cap_add'])).readonly, 'inline list');
  assert.equal(flow.byId.get(id(['services', 'flow', 'command'])).readonly, null);
  assert.equal(flow.byId.get(id(['services', 'flow', 'command'])).value.items.length, 3);
});

test('documents the form view refuses', () => {
  const cases = {
    'a:\n\tb: 1\n': /error/,
    'a: 1\n---\nb: 2\n': /error/,
    'a: 1\na: 2\n': /error/,
    'hello\n': /mapping/,
    '- a\n': /mapping/,
    '{a: 1}\n': /mapping/,
    'a: *nope\n': /resolved/,
  };
  for (const [src, re] of Object.entries(cases)) {
    const doc = load(src);
    assert.match(doc.blocked, re, JSON.stringify(src));
    assert.equal(doc.entries.length, 0);
  }
  assert.equal(load('').blocked, '');
  assert.equal(load('# only a comment\n').blocked, '');
});

test('normalize strips a BOM and CRLF', () => {
  assert.deepEqual(normalize('\ufeffa: 1\r\nb: 2\r\n'), { text: 'a: 1\nb: 2\n', hadBOM: true, hadCRLF: true });
  const crlf = fixture('basic.yaml').replace(/\n/g, '\r\n');
  const doc = load(crlf, S.hooks);
  assert.equal(doc.blocked, '');
  assert.equal(doc.text, fixture('basic.yaml'));
  assert.equal(doc.disabled.length, 4);
});
