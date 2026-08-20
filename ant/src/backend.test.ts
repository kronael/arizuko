// Backend selection contract (spec 6/7 § "The connector seam"): unset
// ARIZUKO_BACKEND runs claude; an unknown value throws so the spawn fails
// loudly to the user instead of silently running the wrong harness.

import { test, expect } from 'bun:test';
import { Readable } from 'stream';
import { jsonLines, selectBackend } from './backend.js';

test('selectBackend: unset defaults to claude', () => {
  expect(selectBackend(undefined).name).toBe('claude');
  expect(selectBackend('').name).toBe('claude');
});

test('selectBackend: each occupant by name', () => {
  expect(selectBackend('claude').name).toBe('claude');
  expect(selectBackend('codex').name).toBe('codex');
  expect(selectBackend('pi').name).toBe('pi');
});

test('selectBackend: unknown value throws — no silent fallback', () => {
  expect(() => selectBackend('gemini')).toThrow('unknown ARIZUKO_BACKEND');
});

test('jsonLines: parses JSONL, skips non-JSON banners, keeps an unterminated tail', async () => {
  const stream = Readable.from(['{"a":1}\nbanner line\n{"b"', ':2}\n{"c":3}']);
  const got: unknown[] = [];
  for await (const obj of jsonLines(stream)) got.push(obj);
  expect(got).toEqual([{ a: 1 }, { b: 2 }, { c: 3 }]);
});
