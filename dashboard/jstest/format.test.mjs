import test from 'node:test';
import assert from 'node:assert/strict';
import { parseDocument } from '../vendor/yaml-2.9.1.min.js';
import { formatScalar, formatKey, canon } from '../js/composedoc.js';

// Strings that change meaning when written carelessly: YAML 1.1 booleans, octals and sexagesimals
// (docker compose's parser), indicators, comments, quotes, and whitespace.
const CORPUS = [
  'x', 'yes', 'no', 'on', 'Off', 'y', 'n', '~', 'null', 'NULL', '', '0755', '0o17', '0x1F', '1_000', '22:22', '1e3',
  '.5', '.inf', '-.inf', '.nan', '3.10', '80:80', '8080:80/udp', ' lead', 'trail ', '  ', 'a: b', 'a:b', 'a #b', 'a#b',
  '#x', '- x', '-x', '[x]', '{x}', 'x, y', '*x', '&x', '!x', '%x', '@x', '`x', "'", '"', "it's", 'say "hi"', '\\',
  'C:\\path', '${VAR}', '$$HOME', 'true', 'False', '-1', '+1', '1.0', '0', '08', '1:2:3', '? x', ': x', 'x:', '--- x',
  '...', '<<', '=', 'a=b c', 'ünïcödé', '日本', 'x\u2028y', 'tab\there', 'a\nb', 'a\n', 'a\n\n', '\nstarts', ' \nx',
  'x\n y', 'trailing \nline', 'CMD-SHELL pg_isready -U app', 'http://localhost:8080/health?x=1#frag',
];

const ALLOWS = [{}, { number: true, boolean: true }];

function readBack(src, ctx, version) {
  const d = parseDocument(src, { version });
  if (d.errors.length) return { error: d.errors[0].message };
  const js = d.toJS();
  return { value: ctx === 'value' ? js.k : js[0] };
}

test('formatScalar round-trips under YAML 1.1 and 1.2', () => {
  for (const s of CORPUS) {
    for (const ctx of ['value', 'item', 'flow']) {
      for (const allow of ALLOWS) {
        const out = formatScalar(s, { ctx, allow, indent: 2 });
        const src = ctx === 'value' ? `k: ${out}\n` : ctx === 'item' ? `- ${out}\n` : `[${out}]\n`;
        for (const version of ['1.1', '1.2']) {
          const got = readBack(src, ctx, version);
          const label = `${JSON.stringify(s)} as ${ctx} ${JSON.stringify(allow)} (YAML ${version}) → ${JSON.stringify(out)}`;
          assert.equal(got.error, undefined, `${label}: ${got.error}`);
          if (typeof got.value === 'string') assert.equal(got.value, s, label);
          else {
            // Only a value the schema allows as a number/boolean may read back as one, and then as
            // exactly the typed text.
            assert.ok((typeof got.value === 'number' && allow.number) || (typeof got.value === 'boolean' && allow.boolean), label);
            assert.equal(String(got.value), s, label);
          }
        }
      }
    }
  }
});

test('formatScalar keeps quote styles and prefers plain text', () => {
  assert.equal(formatScalar('nginx:1.27'), 'nginx:1.27');
  assert.equal(formatScalar('80:80'), '80:80');
  assert.equal(formatScalar('22:22'), '"22:22"');
  assert.equal(formatScalar('x', { style: 'QUOTE_DOUBLE' }), '"x"');
  assert.equal(formatScalar("it's", { style: 'QUOTE_SINGLE' }), "'it''s'");
  assert.equal(formatScalar('say "hi"'), 'say "hi"'); // quotes mid-string are fine in plain text
  assert.equal(formatScalar('"hi" there'), `'"hi" there'`);
  assert.equal(formatScalar('true'), '"true"');
  assert.equal(formatScalar('true', { allow: { boolean: true } }), 'true');
  assert.equal(formatScalar('10', { allow: { number: true } }), '10');
  assert.equal(formatScalar('a\nb', { indent: 4 }), '|-\n    a\n    b');
  assert.equal(formatScalar('a\nb\n', { indent: 2 }), '|\n  a\n  b');
});

test('formatKey quotes only keys that need it', () => {
  const keys = ['TZ', 'traefik.http.routers.web.rule', 'a b', 'a: b', '#x', '1', 'true', 'yes', '<<', '', ' x', 'x-custom', '-x', '[x]', 'ü'];
  for (const k of keys) {
    for (const ctx of ['block', 'flow']) {
      const out = formatKey(k, ctx);
      for (const version of ['1.1', '1.2']) {
        const src = ctx === 'flow' ? `{${out}: v}\n` : `${out}: v\n`;
        const d = parseDocument(src, { version });
        assert.equal(d.errors.length, 0, `${JSON.stringify(k)} → ${out}`);
        assert.equal(canon(Object.keys(d.toJS())), canon([k]), `${JSON.stringify(k)} → ${out} (YAML ${version})`);
      }
    }
  }
  assert.equal(formatKey('TZ'), 'TZ');
  assert.equal(formatKey('a: b'), '"a: b"');
});
