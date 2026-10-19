// Harness selection contract (spec 6/16): unset ARIZUKO_BACKEND runs claude; an
// unknown value throws so the spawn fails loudly instead of silently running the
// wrong harness. The profiles are the declared capability matrix.

import { test, expect } from 'bun:test';
import { Readable } from 'stream';
import { driveCli, jsonLines, selectHarness } from './backend.js';
import type { CliReducer, Frame, ResultFrame } from './backend.js';
import { piHarness } from './pi.js';

test('selectHarness: unset defaults to claude', () => {
  expect(selectHarness(undefined).name).toBe('claude');
  expect(selectHarness('').name).toBe('claude');
});

test('selectHarness: each occupant by name', () => {
  expect(selectHarness('claude').name).toBe('claude');
  expect(selectHarness('codex').name).toBe('codex');
  expect(() => selectHarness('pi')).toThrow('cannot deliver through MCP');
});

test('selectHarness: unknown value throws — no silent fallback', () => {
  expect(() => selectHarness('gemini')).toThrow('unknown ARIZUKO_BACKEND');
});

// The conformance matrix rows of spec 6/16 that are capability declarations.
// A capability that disappears must show up as a changed value here, not as a
// feature quietly not happening.
test('profiles declare the measured capability matrix', () => {
  expect(selectHarness('claude').profile).toEqual({
    continuity: 'native',
    tools: 'mcp',
    guard: 'write-time',
    steering: 'mid-turn',
    manual: 'native',
    status: 'streaming',
  });
  // manual 'rendered': codex and pi have no convention that finds
  // ~/.claude/CLAUDE.md, so the chassis renders it (BUGS L130).
  expect(selectHarness('codex').profile).toEqual({
    continuity: 'native',
    tools: 'mcp',
    guard: 'load-time',
    steering: 'between-turn',
    manual: 'rendered',
    status: 'streaming',
  });
  // pi's guard became write-time when the extension's input mapping was fixed;
  // continuity became native when --session-dir + --session landed.
  expect(piHarness.profile).toEqual({
    continuity: 'native',
    tools: 'none',
    guard: 'write-time',
    steering: 'between-turn',
    manual: 'rendered',
    status: 'streaming',
  });
});

test('jsonLines: parses JSONL, skips non-JSON banners, keeps an unterminated tail', async () => {
  const stream = Readable.from(['{"a":1}\nbanner line\n{"b"', ':2}\n{"c":3}']);
  const got: unknown[] = [];
  for await (const obj of jsonLines(stream)) got.push(obj);
  expect(got).toEqual([{ a: 1 }, { b: 2 }, { c: 3 }]);
});

// A reducer that only synthesizes the terminal at exit — the shape codex and
// pi share, minus their stream mapping.
function tailReducer(): CliReducer {
  return {
    done: false,
    push: () => [],
    finish: (code, tail): ResultFrame => ({
      kind: 'result',
      ok: false,
      text: tail.trim() || `exited ${code}`,
    }),
  };
}

async function collect(gen: AsyncGenerator<Frame>): Promise<Frame[]> {
  const out: Frame[] = [];
  for await (const f of gen) out.push(f);
  return out;
}

// A backend CLI missing from the image emits 'error' on the child, which is an
// uncaught-exception CRASH without a listener — the turn died with no result
// frame, the exact silence the seam forbids.
test('driveCli: a spawn failure yields one error result naming the command', async () => {
  const frames = await collect(driveCli(
    'arizuko-no-such-cli', [], { cwd: '/tmp', env: {} }, tailReducer(), () => {},
  ));
  expect(frames).toHaveLength(1);
  const f = frames[0];
  if (f.kind !== 'result') throw new Error('expected result frame');
  expect(f.ok).toBe(false);
  expect(f.text).toContain('arizuko-no-such-cli');
});

// A child that exits before draining a large prompt EPIPEs the stdin write; an
// unhandled stream 'error' crashed ant the same way.
test('driveCli: a child that never reads a large stdin still finishes the turn', async () => {
  const frames = await collect(driveCli(
    'sh', ['-c', 'exit 3'],
    { cwd: '/tmp', env: { PATH: '/usr/bin:/bin' }, stdin: 'x'.repeat(1 << 20) },
    tailReducer(), () => {},
  ));
  expect(frames).toHaveLength(1);
  const f = frames[0];
  if (f.kind !== 'result') throw new Error('expected result frame');
  expect(f.ok).toBe(false);
});
