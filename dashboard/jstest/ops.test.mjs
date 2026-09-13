import test from 'node:test';
import assert from 'node:assert/strict';
import { load, apply, verifyEdit } from '../js/composedoc.js';
import { S, fixture, fixtureNames, id, rng } from './helpers.mjs';

const run = (doc, ops) => apply(doc, ops, S.hooks);

function ok(doc, ops) {
  const r = run(doc, ops);
  assert.ok(r.ok, `expected success, got: ${r.error}${r.bug ? ' ' + r.bug.stack : ''}`);
  return r;
}

function refused(doc, ops, re) {
  const r = run(doc, ops);
  assert.equal(r.ok, false, 'expected a refusal');
  assert.equal(r.bug, undefined, r.bug && r.bug.stack);
  if (re) assert.match(r.error, re);
  return r;
}

test('setting a value to what it already shows changes nothing', () => {
  for (const name of fixtureNames) {
    const text = fixture(name);
    const doc = load(text, S.hooks);
    for (const e of doc.entries) {
      if (e.readonly || !['scalar', 'block'].includes(e.value.kind)) continue;
      assert.equal(ok(doc, { op: 'set', id: e.id, value: e.value.display }).text, text, `${name} ${e.id}`);
    }
  }
});

// Where disable → enable legitimately doesn't reproduce the original bytes.
const NORMALIZED = new Set([
  'basic.yaml e:["services","web","labels"]', // `labels:` (null, which compose rejects) comes back as `labels: {}`
  'flow.yaml e:["services","flow","extra_hosts"]', // likewise `extra_hosts: []`
  'indentless.yaml e:["services","list","volumes",1,"type"]', // a bare `-` item is rejoined onto one line
]);

test('disable then enable restores every entry byte for byte', () => {
  let toggled = 0;
  for (const name of fixtureNames) {
    const text = fixture(name);
    const doc = load(text, S.hooks);
    for (const e of doc.entries) {
      const off = run(doc, { op: 'disable', id: e.id });
      if (!off.ok) {
        // The refusals are deliberate: the last top-level entry, anchors used elsewhere, and a
        // block with a less-indented line inside.
        assert.equal(off.bug, undefined, off.bug && off.bug.stack);
        assert.match(off.error, /last top-level|anchor|less indented/, `${name} ${e.id}: ${off.error}`);
        continue;
      }
      const d = off.doc.disabled.find((x) => (x.line === e.line || x.line === e.line + 1) && x.key === e.key);
      assert.ok(d, `${name} ${e.id}: not detected after disabling\n${off.text}`);
      const on = ok(off.doc, { op: 'enable', id: d.id });
      toggled++;
      if (!NORMALIZED.has(`${name} ${e.id}`)) assert.equal(on.text, text, `${name} ${e.id}`);
    }
  }
  assert.ok(toggled > 150, `only ${toggled} toggles ran`);
});

test('disabling a service comments out its whole block, disabled children included', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const r = ok(doc, { op: 'disable', id: id(['services', 'web']) });
  const lines = r.text.split('\n').slice(2, 22);
  assert.ok(lines.every((l) => l === '  #' || l.startsWith('  # ')), lines.join('\n'));
  assert.ok(r.text.includes('  #     # - "8443:443"'));
  assert.equal(r.doc.disabled.find((d) => d.key === 'web').line, 2);
  assert.equal(r.doc.js.services.web, undefined);
});

test('disabling the last item leaves an empty list compose accepts', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const r = ok(doc, { op: 'disable', id: id(['services', 'web', 'ports', 0]) });
  assert.match(r.text, /\n {4}ports: \[\]\n {6}# - "8080:80"\n {6}# - "8443:443"\n/);
  assert.deepEqual(r.doc.js.services.web.ports, []);
  const back = ok(r.doc, { op: 'enable', id: r.doc.disabled.find((d) => d.scalar === '8080:80').id });
  assert.equal(back.text, fixture('basic.yaml'));
});

test('the first key of a `- key: value` item moves off the dash line and back', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const r = ok(doc, { op: 'disable', id: id(['services', 'web', 'volumes', 0, 'type']) });
  assert.match(r.text, /\n {6}-\n {8}# type: bind\n {8}source: \.\/html\n/);
  const d = r.doc.disabled.find((x) => x.key === 'type');
  assert.equal(ok(r.doc, { op: 'enable', id: d.id }).text, fixture('basic.yaml'));
  // Removing it hoists the next key onto the dash instead.
  const removed = ok(doc, { op: 'remove', id: id(['services', 'web', 'volumes', 0, 'type']) });
  assert.match(removed.text, /\n {6}- source: \.\/html\n {8}target:/);
});

test('refusals', () => {
  const basic = load(fixture('basic.yaml'), S.hooks);
  refused(load('services:\n  a:\n    image: x\n', S.hooks), { op: 'disable', id: id(['services']) }, /last top-level/);
  refused(load(fixture('anchors.yaml'), S.hooks), { op: 'disable', id: id(['x-common']) }, /&common/);
  refused(load(fixture('commented-duplicate.yaml'), S.hooks), { op: 'enable', id: load(fixture('commented-duplicate.yaml'), S.hooks).disabled[0].id }, /already set/);
  refused(basic, { op: 'rename', id: id(['services', 'db']), key: 'web' }, /already exists/);
  refused(basic, { op: 'rename', id: id(['services', 'db']), key: 'bad name' }, /isn't a valid name/);
  refused(basic, { op: 'add', parent: id(['services', 'web']), key: 'imagee', value: 'x' }, /doesn't have/);
  refused(basic, { op: 'set', id: id(['services', 'web', 'ports']), value: 'x' }, /YAML view/);
  refused(basic, { op: 'set', id: 'e:["gone"]', value: 'x' }, /changed underneath/);
  refused(load(fixture('flow.yaml'), S.hooks), { op: 'add', parent: id(['services', 'flow', 'dns']), value: '8.8.8.8' }, /inline list/);
});

test('set formats values so they read back unchanged', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  const set = (p, value) => ok(doc, { op: 'set', id: id(p), value });
  assert.match(set(['services', 'web', 'image'], 'nginx:1.28').text, /image: nginx:1\.28 # pinned\n/);
  assert.match(set(['services', 'web', 'ports', 0], '22:22').text, /- "22:22"\n/); // keeps its double quotes
  assert.match(set(['services', 'web', 'restart'], 'yes').text, /restart: "yes"\n/);
  assert.match(set(['services', 'web', 'environment', 'TZ'], 'a # b').text, /TZ: "a # b"\n/);
  // A null value gets one, before any trailing comment.
  const labels = load('services:\n  web:\n    image: a\n    user: # who\n', S.hooks);
  assert.match(ok(labels, { op: 'set', id: id(['services', 'web', 'user']), value: '1000' }).text, /user: "1000" # who\n/);
  // Multi-line text becomes a literal block; the trailing comment moves to the header.
  const multi = set(['services', 'web', 'image'], 'line one\nline two');
  assert.match(multi.text, /image: \|- # pinned\n {6}line one\n {6}line two\n {4}restart:/);
  assert.equal(multi.doc.js.services.web.image, 'line one\nline two');
  // And back to one line.
  const single = ok(multi.doc, { op: 'set', id: id(['services', 'web', 'image']), value: 'nginx' });
  assert.match(single.text, /image: nginx\n {4}restart:/);
});

test('booleans and numbers stay unquoted only where the schema allows them', () => {
  const doc = load('services:\n  web:\n    image: a\n    privileged: false\n    cpus: 1\n    user: nobody\n', S.hooks);
  assert.match(ok(doc, { op: 'set', id: id(['services', 'web', 'privileged']), value: 'true' }).text, /privileged: true\n/);
  assert.match(ok(doc, { op: 'set', id: id(['services', 'web', 'cpus']), value: '1.5' }).text, /cpus: 1\.5\n/);
  assert.match(ok(doc, { op: 'set', id: id(['services', 'web', 'cpus']), value: '3.10' }).text, /cpus: "3\.10"\n/);
  assert.match(ok(doc, { op: 'set', id: id(['services', 'web', 'user']), value: '1000' }).text, /user: "1000"\n/);
});

test('add places entries like their neighbours', () => {
  const basic = load(fixture('basic.yaml'), S.hooks);
  // After the list's last (disabled) child.
  let r = ok(basic, { op: 'add', parent: id(['services', 'web', 'ports']), value: '9090:90' });
  assert.match(r.text, /# - "8443:443"\n {6}- 9090:90\n {4}environment:/);
  // Into a null field: a mapping by key.
  r = ok(basic, { op: 'add', parent: id(['services', 'web', 'labels']), key: 'traefik.enable', value: 'true' });
  assert.match(r.text, /labels:\n {6}traefik\.enable: true\n/); // label values may be booleans
  // A new service with its image in one step.
  r = ok(basic, [
    { op: 'add', parent: id(['services']), key: 'cache', kind: 'map' },
    { op: 'add', parent: id(['services', 'cache']), key: 'image', value: 'redis:7' },
  ]);
  assert.match(r.text, /\n {2}cache:\n {4}image: redis:7\nvolumes:/);
  // Into an empty inline list, which becomes a block list.
  const flow = load(fixture('flow.yaml'), S.hooks);
  r = ok(flow, { op: 'add', parent: id(['services', 'flow', 'ports']), value: '81:81' });
  assert.match(r.text, /ports:\n {6}# - "80:80"\n {6}- 81:81\n/);
  // Indentless and 4-space documents.
  r = ok(load(fixture('indentless.yaml'), S.hooks), { op: 'add', parent: id(['services', 'list', 'command']), value: '--verbose' });
  assert.match(r.text, /command:\n {4}- serve\n {4}- --verbose\n/);
  r = ok(load(fixture('indent4.yaml'), S.hooks), { op: 'add', parent: id(['services', 'wide']), key: 'restart', value: 'always' });
  assert.match(r.text, /- front\n {8}restart: always\n/);
  // A top-level section at the end of the file.
  r = ok(basic, { op: 'add', parent: 'root', key: 'networks', kind: 'map' });
  assert.ok(r.text.endsWith('volumes:\n  data: {}\nnetworks: {}\n'));
});

test('remove deletes an entry and its disabled children', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  let r = ok(doc, { op: 'remove', id: id(['services', 'web', 'ports']) });
  assert.ok(!r.text.includes('8443'));
  r = ok(doc, { op: 'remove', id: doc.disabled.find((d) => d.key === 'redis').id });
  assert.ok(!r.text.includes('redis'));
  assert.equal(r.doc.disabled.length, 3);
  r = ok(doc, { op: 'remove', id: id(['services', 'db', 'environment', 0]) });
  assert.match(r.text, /environment: \[\]\n {6}# - POSTGRES_USER=app\n/);
});

test('inline lists edit item by item and convert to block', () => {
  const doc = load(fixture('flow.yaml'), S.hooks);
  const e = doc.byId.get(id(['services', 'flow', 'command']));
  const items = e.value.items.map((it) => ({ ...it }));
  items[2] = { value: 'dev server' };
  items.push({ value: '--port=80' });
  let r = ok(doc, { op: 'flowItems', id: e.id, items });
  assert.match(r.text, /command: \["npm", "run", "dev server", "--port=80"\]\n/);
  r = ok(doc, { op: 'toBlock', id: id(['services', 'flow', 'healthcheck', 'test']) });
  assert.match(r.text, /test: # spaced flow\n {8}- "CMD-SHELL"\n {8}- "pg_isready -U app"\n/);
});

test('rename keeps the value and quotes keys that need it', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  let r = ok(doc, { op: 'rename', id: id(['services', 'web', 'environment', 'TZ']), key: 'TIME ZONE' });
  assert.match(r.text, /\n {6}TIME ZONE: Europe\/Paris\n/); // a plain key may contain spaces
  r = ok(doc, { op: 'rename', id: id(['services', 'web', 'environment', 'TZ']), key: 'a: b' });
  assert.match(r.text, /\n {6}"a: b": Europe\/Paris\n/);
  assert.equal(r.doc.js.services.web.environment['a: b'], 'Europe/Paris');
});

test('verifyEdit rejects text that parses but means something else', () => {
  const doc = load(fixture('basic.yaml'), S.hooks);
  // Commenting the dash line of `- type: bind` naively turns the list into garbage or a map.
  const naive = doc.text.replace('      - type: bind', '      # - type: bind');
  assert.equal(verifyEdit(doc, { text: naive, expect: (js) => { delete js.services.web.volumes[0].type; return js; } }, S.hooks), null);
  const same = verifyEdit(doc, { text: doc.text.replace('image: nginx:1.27', 'image: nginx:1.27 '), expect: (js) => js }, S.hooks);
  assert.ok(same);
});

// Random edits must never crash, never produce text the view can't load, and always leave the
// data exactly as the operation declared (apply's own check) — across every fixture.
test('random operation sequences stay safe', () => {
  const values = ['x', '', 'yes', '0755', '22:22', 'a: b', 'a #b', '#x', '${VAR}', "it's", 'say "hi"', 'multi\nline', ' lead', 'true', '8080:80', 'ünï'];
  let applied = 0;
  for (const name of fixtureNames) {
    const random = rng(name.length * 7919);
    let doc = load(fixture(name), S.hooks);
    for (let step = 0; step < 60; step++) {
      const all = [...doc.entries, ...doc.disabled];
      if (!all.length) break;
      const e = all[Math.floor(random() * all.length)];
      const value = values[Math.floor(random() * values.length)];
      const choice = Math.floor(random() * 6);
      let op;
      if (e.disabled) op = choice < 4 ? { op: 'enable', id: e.id } : { op: 'remove', id: e.id };
      else if (choice === 0) op = { op: 'set', id: e.id, value };
      else if (choice === 1 || choice === 2) op = { op: 'disable', id: e.id };
      else if (choice === 3) op = { op: 'remove', id: e.id };
      else if (choice === 4) op = { op: 'add', parent: e.id, key: e.value && (e.value.kind === 'seq' || e.value.kind === 'flowseq') ? undefined : 'X_' + step, value };
      else op = { op: 'rename', id: e.id, key: 'K' + step };
      const r = run(doc, op);
      assert.equal(r.bug, undefined, `${name} step ${step} ${JSON.stringify(op)}: ${r.bug && r.bug.stack}\n${doc.text}`);
      if (!r.ok) continue;
      const reloaded = load(r.text, S.hooks);
      assert.equal(reloaded.blocked, '', `${name} step ${step} ${JSON.stringify(op)} produced unloadable text`);
      doc = r.doc;
      applied++;
    }
  }
  assert.ok(applied > 300, `only ${applied} operations applied`);
});
