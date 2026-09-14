import test from 'node:test';
import assert from 'node:assert/strict';
import { load, lint } from '../js/composedoc.js';
import { buildRows, pickerFields, addKindFor } from '../js/composerows.js';
import { S, fixture, id } from './helpers.mjs';

const kinds = (node) => [...(S.kinds(node) || ['any'])].sort().join(',');

test('field kinds follow the schema, through $ref and oneOf', () => {
  const svc = S.branch(S.child(S.json, 'services'), 'map');
  const service = S.child(svc, 'web');
  const field = (k) => S.child(S.branch(service, 'map'), k);
  assert.equal(kinds(field('image')), 'scalar');
  assert.equal(kinds(field('ports')), 'seq');
  assert.equal(kinds(field('environment')), 'map,seq');
  assert.equal(kinds(field('command')), 'null,scalar,seq');
  assert.equal(kinds(field('healthcheck')), 'map');
  assert.equal(kinds(field('build')), 'map,scalar');
  assert.equal(kinds(field('x-anything')), 'any');
  assert.equal(field('imagee'), undefined);
  const portItem = S.resolve(S.branch(field('ports'), 'seq').items);
  assert.equal(kinds(portItem), 'map,scalar');
  assert.ok(S.child(S.branch(portItem, 'map'), 'published'));
  // The site's description wins over the $ref target's.
  assert.match(S.describe(field('environment')).description, /environment/i);
});

test('schema lookup by entry', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const at = (p) => S.at(doc, doc.byId.get(id(p)));
  assert.equal(kinds(at(['services', 'web', 'ports', 0])), 'map,scalar');
  assert.equal(kinds(at(['services', 'web', 'volumes', 0, 'type'])), 'scalar');
  assert.deepEqual(S.describe(at(['services', 'web', 'volumes', 0, 'type'])).enum.slice(0, 3), ['bind', 'volume', 'tmpfs']);
  assert.deepEqual(S.suggestions('restart', at(['services', 'web', 'restart'])), ['no', 'always', 'on-failure', 'unless-stopped']);
  assert.deepEqual(S.scalarAllow(at(['services', 'web', 'healthcheck', 'interval'])), { number: false, boolean: false });
});

test('checkKey and enableValue', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const services = doc.byId.get(id(['services']));
  const web = doc.byId.get(id(['services', 'web']));
  assert.match(S.checkKey(services, 'bad name!', doc), /valid name/);
  assert.equal(S.checkKey(services, 'good-name_1', doc), null);
  assert.match(S.checkKey(web, 'imagee', doc), /doesn't have/);
  assert.equal(S.checkKey(web, 'x-note', doc), null);
  assert.equal(S.checkKey(doc.byId.get(id(['services', 'web', 'environment'])), 'ANY_THING', doc), null);
  const header = (src) => {
    const d = load(src, S.hooks);
    return S.enableValue(d.byId.get(d.disabled[0].parent), d.disabled[0], d);
  };
  assert.equal(header('services:\n  web:\n    image: a\n    # ports:\n'), '[]');
  assert.equal(header('services:\n  web:\n    image: a\n    # healthcheck:\n'), '{}');
  assert.equal(header('services:\n  web:\n    image: a\n    # environment:\n    #   - A=1\n'), '[]');
  assert.equal(header('services:\n  web:\n    image: a\n    # environment:\n    #   A: "1"\n'), '{}');
  assert.equal(header('services:\n  web:\n    image: a\n    # command:\n'), null);
});

test('picker lists missing fields, common ones first, and offers to enable commented ones', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const web = pickerFields(doc, S, id(['services', 'web']));
  const keys = web.map((f) => f.key);
  assert.ok(!keys.includes('image') && !keys.includes('ports'));
  assert.deepEqual(keys.slice(0, 3), ['build', 'container_name', 'env_file']);
  assert.ok(keys.length > 80);
  assert.equal(web.find((f) => f.key === 'healthcheck'), undefined);
  const services = pickerFields(doc, S, 'root').map((f) => f.key);
  assert.ok(services.includes('networks') && !services.includes('services'));
  const db = pickerFields(load('services:\n  db:\n    image: a\n    # restart: always\n', S.hooks), S, id(['services', 'db']));
  assert.ok(db.find((f) => f.key === 'restart').disabledId);
  assert.equal(addKindFor(S.kinds(S.child(S.branch(S.child(S.branch(S.child(S.json, 'services'), 'map'), 'x'), 'map'), 'environment')), 'environment'), 'map');
  assert.equal(addKindFor(new Set(['map', 'seq']), 'depends_on'), 'seq');
});

test('rows: structure, routing and notes', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const rows = buildRows(doc, S, {});
  const row = (rid) => rows.find((r) => r.id === rid);
  assert.equal(row(id(['name'])).t, 'field');
  assert.equal(row(id(['services'])).t, 'section');
  assert.equal(row(id(['services', 'web'])).isService, true);
  assert.equal(row(id(['services', 'web', 'environment', 'TZ'])).t, 'kv');
  assert.equal(row(id(['services', 'web', 'ports', 0])).t, 'item');
  assert.equal(row(id(['services', 'web', 'healthcheck', 'test'])).flow, true);
  assert.equal(rows.filter((r) => r.entryId === id(['services', 'web', 'healthcheck', 'test']) && r.t === 'flowitem').length, 4);
  assert.equal(row(id(['services', 'web', 'volumes', 0])).label, 'bind ./html → /usr/share/nginx/html');
  const redis = rows.find((r) => r.t === 'disabled' && r.label === 'redis');
  assert.match(redis.note, /Save & redeploy removes its container while "Remove orphaned containers" is checked/);
  assert.equal(redis.summary, 'redis:7');
  assert.ok(rows.some((r) => r.t === 'add' && r.addKind === 'named' && r.label === 'Add service'));
  assert.ok(rows.some((r) => r.t === 'add' && r.addKind === 'kv' && r.entryId === id(['services', 'web', 'environment'])));
  // The null `labels:` renders as an empty group, not a text box.
  assert.equal(row(id(['services', 'web', 'labels'])).t, 'group');

  // Disabling the only field of a long-syntax item targets the whole item.
  const single = load('services:\n  web:\n    image: a\n    ports:\n      - target: 80\n', S.hooks);
  const target = buildRows(single, S, {}).find((r) => r.id === id(['services', 'web', 'ports', 0, 'target']));
  assert.equal(target.toggleTarget, id(['services', 'web', 'ports', 0]));

  // Groups collapse on request; with more than three services they start collapsed.
  const collapsed = buildRows(doc, S, { toggled: new Set([id(['services', 'web'])]) });
  assert.equal(collapsed.find((r) => r.id === id(['services', 'web'])).open, false);
  assert.ok(!collapsed.some((r) => r.id === id(['services', 'web', 'image'])));
  const many = load('services:\n' + ['a', 'b', 'c', 'd'].map((n) => `  ${n}:\n    image: x\n`).join(''), S.hooks);
  assert.equal(buildRows(many, S, {}).find((r) => r.id === id(['services', 'a'])).open, false);
});

test('unknown keys are flagged', () => {
  const doc = load('services:\n  web:\n    image: a\n    imagee: b\n    x-note: c\n', S.hooks);
  assert.deepEqual(S.unknownKeys(doc).map((e) => e.key), ['imagee']);
  assert.match(buildRows(doc, S, {}).find((r) => r.label === 'imagee').warn, /Not a compose field/);
});

test('lint catches what compose would reject', () => {
  const js = load(fixture('basic.yaml')).js;
  assert.deepEqual(lint(js), []);
  const broken = structuredClone(js);
  delete broken.services.db;
  delete broken.volumes;
  broken.services.web.networks = ['front'];
  const warnings = lint(broken).join('\n');
  assert.match(warnings, /depends on "db"/);
  assert.match(warnings, /volume "data"/);
  assert.match(warnings, /network "front"/);
});
