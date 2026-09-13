import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { parseEnv, envRows, envOps, quoteValue, readValue, isSecretKey } from '../js/dotenv.js';

const SAMPLE = [
  '#### app',
  '# ── Database ──────────',
  '# For an external DB, point DB_HOST at it',
  'DB_HOST=localhost # default',
  'export DB_PORT=5432',
  "DB_PASSWORD='s3cret value'",
  'DB_NAME: app',
  '# DB_USER=admin',
  '#DEBUG=true',
  '# Note: keep this file out of git',
  'GREETING="hello',
  'world"',
  'INHERITED',
  'DB_HOST=db',
  'BAD KEY=1',
  '',
].join('\n');

test('classifies lines like compose reads them', () => {
  const { items } = parseEnv(SAMPLE);
  const summary = items.map((it) => `${it.t}${it.key ? ':' + it.key : ''}`);
  assert.deepEqual(summary, [
    'comment', 'comment', 'comment', 'var:DB_HOST', 'var:DB_PORT', 'var:DB_PASSWORD', 'var:DB_NAME',
    'disabled:DB_USER', 'disabled:DEBUG', 'comment', 'var:GREETING', 'bare:INHERITED', 'var:DB_HOST', 'invalid', 'blank',
  ]);
  const byKey = (k) => items.find((it) => it.key === k && it.t === 'var');
  assert.equal(byKey('DB_HOST').value, 'localhost');
  assert.equal(byKey('DB_HOST').shadowed, true); // the later DB_HOST=db wins
  assert.equal(byKey('DB_PASSWORD').quote, "'");
  assert.equal(byKey('GREETING').multiline, true);
  assert.equal(readValue(byKey('GREETING')), 'hello\nworld');
});

test('rows: sections, help text, secrets', () => {
  const rows = envRows(parseEnv(SAMPLE));
  assert.deepEqual(rows.filter((r) => r.t === 'section').map((r) => r.text), ['app', 'Database']);
  const host = rows.find((r) => r.key === 'DB_HOST');
  assert.equal(host.help, 'For an external DB, point DB_HOST at it');
  assert.equal(rows.find((r) => r.key === 'DB_PASSWORD').secret, true);
  assert.equal(rows.find((r) => r.key === 'DB_PORT').secret, false);
  assert.equal(rows.find((r) => r.t === 'invalid').error, 'key cannot contain a space');
  assert.equal(rows.filter((r) => r.t === 'disabled').length, 2);
});

test('secret-looking keys', () => {
  for (const k of ['DB_PASSWORD', 'PASS', 'API_KEY', 'APIKEY', 'FEEDER_KEY', 'GITHUB_TOKEN', 'SECRET', 'AUTH_SECRET', 'PRIVATE_KEY']) assert.ok(isSecretKey(k), k);
  for (const k of ['KEYCLOAK_URL', 'MONKEY', 'TZ', 'PUID', 'PASSTHROUGH']) assert.ok(!isSecretKey(k), k);
});

test('toggling a variable twice restores the file', () => {
  const model = parseEnv(SAMPLE);
  for (const it of model.items) {
    if (!['var', 'disabled'].includes(it.t) || it.multiline) continue;
    const once = envOps.toggle(model, it.start);
    const twice = envOps.toggle(parseEnv(once), it.start);
    if (it.t === 'disabled' && it.hash !== '# ') continue; // `#DEBUG` comes back as `# DEBUG`
    assert.equal(twice, SAMPLE, `line ${it.start + 1}`);
  }
  assert.throws(() => envOps.toggle(model, 10), /several lines/);
  assert.throws(() => envOps.toggle(model, 12), /KEY=value/);
});

test('edits keep quote styles and inline comments', () => {
  const model = parseEnv(SAMPLE);
  const line = (text, n) => text.split('\n')[n];
  assert.equal(line(envOps.setValue(model, 3, 'db.internal'), 3), 'DB_HOST=db.internal # default');
  assert.equal(line(envOps.setValue(model, 5, "it's"), 5), "DB_PASSWORD='it\\'s'");
  assert.equal(line(envOps.setValue(model, 3, 'a #b'), 3), "DB_HOST='a #b' # default");
  assert.equal(line(envOps.setValue(model, 7, 'root'), 7), '# DB_USER=root');
  assert.equal(line(envOps.setKey(model, 4, 'PG_PORT'), 4), 'export PG_PORT=5432');
  assert.throws(() => envOps.setKey(model, 4, 'BAD KEY'), /Names use/);
  assert.equal(envOps.remove(model, 13).split('\n').length, SAMPLE.split('\n').length - 1);
  assert.ok(envOps.add(model, 'NEW', 'x y').endsWith("BAD KEY=1\nNEW=x y\n"));
  assert.equal(envOps.add(parseEnv(''), 'A', '1'), 'A=1\n');
});

test('quoteValue reads back as the same string', () => {
  const values = ['plain', '', ' lead', 'trail ', 'a #b', 'a#b', '#x', "it's", 'say "hi"', '"quoted"', "'single'", '${HOME}/x', 'C:\\path\\', 'tab\tin', '=', 'a=b', 'ünï', '$$'];
  for (const v of values) {
    const text = `K=${quoteValue(v)}\n`;
    const it = parseEnv(text).items[0];
    assert.equal(it.t, 'var', `${JSON.stringify(v)} → ${text}`);
    assert.equal(readValue(it), v, `${JSON.stringify(v)} → ${text}`);
  }
});

// Cross-check against docker compose itself: it must read every value the editor writes as the
// string the user typed. Skipped when docker compose isn't installed.
test('docker compose reads the values the editor writes', (t) => {
  try {
    execFileSync('docker', ['compose', 'version'], { stdio: 'ignore' });
  } catch {
    t.skip('docker compose not available');
    return;
  }
  const values = ['plain', ' lead', 'trail ', 'a #b', "it's", 'say "hi"', '"quoted"', "'single'", 'C:\\path\\', 'a=b', 'ünï', 'x # y # z'];
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'la-dotenv-'));
  try {
    let env = parseEnv('');
    let text = '';
    values.forEach((v, i) => {
      text = envOps.add(env, `V${i}`, v);
      env = parseEnv(text);
    });
    fs.writeFileSync(path.join(dir, 'test.env'), text);
    const envMap = values.map((_, i) => `      V${i}: "\${V${i}}"`).join('\n');
    fs.writeFileSync(path.join(dir, 'compose.yaml'), `services:\n  app:\n    image: busybox\n    environment:\n${envMap}\n`);
    const out = execFileSync('docker', ['compose', '-f', path.join(dir, 'compose.yaml'), '--env-file', path.join(dir, 'test.env'), 'config', '--format', 'json'], { encoding: 'utf8' });
    const got = JSON.parse(out).services.app.environment;
    values.forEach((v, i) => assert.equal(got[`V${i}`], v, `V${i}: ${JSON.stringify(v)} written as ${text.split('\n')[i]}`));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
