// Codex backend: drives a fake `codex app-server` (no real auth) and asserts
// the JSON-RPC event stream normalizes per the spec 5/K mapping table. Spawns
// the fixture via CODEX_BIN; never touches a real codex install or tokens.

import { test, expect } from 'bun:test';
import fs from 'fs';
import path from 'path';
import os from 'os';
import { fileURLToPath } from 'url';
import { CodexBackend } from './codex.js';
import { Event } from './types.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, 'codex-fake.fixture.mjs');

// makeWrapper writes an executable shell script that runs the fake harness,
// ignoring the "app-server" arg the backend appends.
function makeWrapper(): { bin: string; cleanup: () => void } {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'arizuko-codex-'));
  const bin = path.join(dir, 'codex');
  fs.writeFileSync(bin, `#!/bin/sh\nexec node ${JSON.stringify(fixture)}\n`);
  fs.chmodSync(bin, 0o755);
  return { bin, cleanup: () => fs.rmSync(dir, { recursive: true, force: true }) };
}

test('codex backend normalizes the app-server event stream', async () => {
  const { bin, cleanup } = makeWrapper();
  process.env.CODEX_BIN = bin;
  try {
    const backend = new CodexBackend();
    const session = await backend.spawn({ prompt: 'hi', cwd: process.cwd() });

    const events: Event[] = [];
    for await (const ev of session.events()) {
      events.push(ev);
      if (ev.final) break;
    }
    await session.close();

    const types = events.map(e => e.type);
    // thread/started → system_init; agentMessage delta+done → assistant×2;
    // mcpToolCall → tool_use; toolResult → tool_result; turn/finished → result.
    expect(types).toEqual([
      'system_init', 'assistant', 'assistant', 'tool_use', 'tool_result', 'result',
    ]);

    const init = events[0];
    expect(init.sessionId).toBe('thr-1');

    const done = events[2];
    expect(done.text).toBe('hello world');

    const result = events[events.length - 1];
    expect(result.final).toBe(true);
    expect(result.status).toBe('success');
    expect(result.text).toBe('hello world');
    expect(result.models?.['gpt-5']).toEqual({
      input: 10, output: 5, cache_read: 2, cache_write: 0, cost_cents: 3,
    });
  } finally {
    delete process.env.CODEX_BIN;
    cleanup();
  }
});

test('codex backend caps advertise live model switching', () => {
  const caps = new CodexBackend().capabilities();
  expect(caps.setModelLive).toBe(true);
});
