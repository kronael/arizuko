// Harness selection contract (spec 6/7 § "The harness seam"): unset
// ARIZUKO_BACKEND runs claude; an unknown value throws so the spawn fails
// loudly to the user instead of silently running the wrong harness. The
// profiles are the declared capability matrix — a rendering of the code, not
// prose beside it.

import { test, expect } from 'bun:test';
import { Readable } from 'stream';
import { jsonLines, selectHarness } from './backend.js';

test('selectHarness: unset defaults to claude', () => {
  expect(selectHarness(undefined).name).toBe('claude');
  expect(selectHarness('').name).toBe('claude');
});

test('selectHarness: each occupant by name', () => {
  expect(selectHarness('claude').name).toBe('claude');
  expect(selectHarness('codex').name).toBe('codex');
  expect(selectHarness('pi').name).toBe('pi');
});

test('selectHarness: unknown value throws — no silent fallback', () => {
  expect(() => selectHarness('gemini')).toThrow('unknown ARIZUKO_BACKEND');
});

// The conformance matrix rows of spec 6/7 that are capability declarations.
// A capability that disappears must show up as a changed value here, not as a
// feature quietly not happening.
test('profiles declare the measured capability matrix', () => {
  expect(selectHarness('claude').profile).toEqual({
    continuity: 'native',
    tools: 'mcp',
    guard: 'write-time',
    steering: 'mid-turn',
    status: 'streaming',
  });
  expect(selectHarness('codex').profile).toEqual({
    continuity: 'native',
    tools: 'mcp',
    guard: 'load-time',
    steering: 'between-turn',
    status: 'streaming',
  });
  expect(selectHarness('pi').profile).toEqual({
    continuity: 'none',
    tools: 'none',
    guard: 'load-time',
    steering: 'between-turn',
    status: 'streaming',
  });
});

test('jsonLines: parses JSONL, skips non-JSON banners, keeps an unterminated tail', async () => {
  const stream = Readable.from(['{"a":1}\nbanner line\n{"b"', ':2}\n{"c":3}']);
  const got: unknown[] = [];
  for await (const obj of jsonLines(stream)) got.push(obj);
  expect(got).toEqual([{ a: 1 }, { b: 2 }, { c: 3 }]);
});
