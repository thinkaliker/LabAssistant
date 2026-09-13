// Shared setup for the dashboard's JS tests: the vendored compose schema and the YAML fixtures.
// Run with `node --test dashboard/jstest/` (or `go test ./dashboard`, which wraps it).

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createSchema } from '../js/composeschema.js';

const here = path.dirname(fileURLToPath(import.meta.url));

export const S = createSchema(JSON.parse(fs.readFileSync(path.join(here, '../vendor/compose-spec.json'), 'utf8')));

export const fixtureNames = fs.readdirSync(path.join(here, 'fixtures')).filter((f) => f.endsWith('.yaml')).sort();

export function fixture(name) {
  return fs.readFileSync(path.join(here, 'fixtures', name), 'utf8');
}

export const id = (p) => 'e:' + JSON.stringify(p);

// mulberry32: a small seeded PRNG so the property tests are reproducible.
export function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
