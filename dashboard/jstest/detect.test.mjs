import test from 'node:test';
import assert from 'node:assert/strict';
import { load } from '../js/composedoc.js';
import { S, fixture } from './helpers.mjs';

// disabledOf summarizes a fixture's detected disabled entries as "parent path > key|[item] @L".
function disabledOf(name, hooks = S.hooks) {
  const doc = load(fixture(name), hooks);
  return doc.disabled.map((d) => {
    const parent = d.parent === 'root' ? '' : JSON.parse(d.parent.slice(2)).join('/');
    return `${parent}>${d.key ?? '[item]'}@${d.L}${d.duplicate ? ' dup' : ''}`;
  });
}

const expected = {
  'all-commented.yaml': ['>volumes@0', '>services@0'],
  'anchors.yaml': [],
  'basic.yaml': ['services/web/ports>[item]@6', 'services/web/environment>DEBUG@6', 'services>redis@2', 'services/db/environment>[item]@6'],
  'commented-duplicate.yaml': ['>services@0 dup'],
  'devices.yaml': ['services/nvr/devices>[item]@6', 'services/nvr/devices>[item]@6'],
  'env-prose.yaml': ['services/feeder/environment>[item]@6'],
  'flow.yaml': ['services/flow/ports>[item]@6', 'services/flow/extra_hosts>[item]@6'],
  'indent4.yaml': ['services/wide/environment>B@12', 'services>other@4'],
  'indentless.yaml': ['services/list/ports>[item]@4'],
  'prose-and-ports.yaml': ['services/printer>ports@4', 'services/printer/volumes>[item]@6', 'services/printer/environment>[item]@6', 'services>postgres@2'],
  'scalars.yaml': [],
  'styles.yaml': ['services/app>command@4', 'services/app>user@4', 'services>worker@2', 'services>cron@2', 'services>jobs@2', 'services/api/ports>[item]@6'],
};

for (const [name, want] of Object.entries(expected)) {
  test(`detects disabled entries in ${name}`, () => {
    assert.deepEqual(disabledOf(name), want);
  });
}

test('disabled blocks keep their whole body', () => {
  const doc = load(fixture('styles.yaml'), S.hooks);
  const worker = doc.disabled.find((d) => d.key === 'worker');
  // The blank `#` line and the nested `# # environment:` stay part of the block.
  assert.equal(worker.fragment, 'worker:\n  image: example/app:1\n\n  command: work\n  # environment:\n  #   QUEUE: default\n');
  const ports = load(fixture('prose-and-ports.yaml'), S.hooks).disabled.find((d) => d.key === 'ports');
  assert.equal(ports.line, 10);
  assert.equal(ports.endLine, 13); // stops before the `#` separator and the note after it
  assert.deepEqual(ports.js, ['8000:8000', '990:990', '2021:2021/udp']);
});

test('prose is not mistaken for config', () => {
  const doc = load(fixture('prose-and-ports.yaml'), S.hooks);
  const keys = doc.disabled.map((d) => d.key);
  for (const prose of ['Usage', 'LINUX', 'macOS/WINDOWS', 'Note', 'Optional', 'MariaDB', 'Start with']) {
    assert.ok(!keys.includes(prose), `${prose} detected as config`);
  }
  // Bullets of words in an environment list, ruler lines, and `# REMOVED: …` notes.
  const env = load(fixture('env-prose.yaml'), S.hooks).disabled;
  assert.deepEqual(env.map((d) => d.scalar), ['FEEDER_KEY_UAT=def456']);
  const devices = load(fixture('devices.yaml'), S.hooks).disabled;
  assert.deepEqual(devices.map((d) => d.scalar), ['/dev/apex_0:/dev/apex_0', '/dev/video11:/dev/video11']);
});

test('a column-0 comment inside a service is not config for the top level', () => {
  const src = 'services:\n  web:\n    image: a\n# networks:\n#   - front\n    restart: always\n';
  const doc = load(src, S.hooks);
  assert.deepEqual(doc.disabled.map((d) => d.key), []);
});

test('config-like prose is rejected by the schema', () => {
  const src = [
    'services:',
    '  web:',
    '    image: a',
    '    # ports: 80 is traefik',
    '    ports:',
    '      - "80:80"',
    '      # - remember to rotate keys',
    '      # - Note: see the wiki',
    '    environment:',
    '      # MariaDB: small footprint',
    '      # Example: FOO=bar',
    '      A: "1"',
    '',
  ].join('\n');
  const doc = load(src, S.hooks);
  assert.deepEqual(doc.disabled, []);
});

test('real field names beat the prose list, and lowercase dict keys with a token value count', () => {
  const src = [
    'services:',
    '  web:',
    '    image: a',
    '    depends_on:',
    '      db:',
    '        condition: service_started',
    '        # required: false',
    '    labels:',
    '      # mode: 0755',
    '      a.b: c',
    '  db:',
    '    image: b',
    '',
  ].join('\n');
  const keys = load(src, S.hooks).disabled.map((d) => d.key);
  assert.deepEqual(keys, ['required', 'mode']);
});

test('without schema hooks, detection falls back to token-like keys', () => {
  const doc = load('a:\n  b: 1\n  # c: 2\n  # Some note: here\n');
  assert.deepEqual(doc.disabled.map((d) => d.key), ['c']);
});
