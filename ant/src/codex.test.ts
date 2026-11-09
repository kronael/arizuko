// codex harness mapping, tested against the RECORDED `codex exec --json`
// stream (codex-cli 0.147.0, captured 2026-08-20 with a live one-turn run) —
// the event shapes are fixtures, not guesses.

import { test, expect } from 'bun:test';
import {
  CodexTurn, codexArgs, codexHarness, codexPrompt, threadTotal, tomlValue,
} from './codex.js';
import fs from 'fs';
import os from 'os';
import path from 'path';
import { buildSystemPrompt, statusBlocks } from './index.js';
import type { Frame } from './backend.js';

// Recorded verbatim: codex exec --json "Reply with exactly: pong"
const FIXTURE = [
  { type: 'thread.started', thread_id: '01a01f62-3568-75d0-96c7-8a48a297b1ff' },
  { type: 'turn.started' },
  { type: 'item.completed', item: { id: 'item_0', type: 'agent_message', text: 'pong' } },
  {
    type: 'turn.completed',
    usage: {
      input_tokens: 29536,
      cached_input_tokens: 9984,
      cache_write_input_tokens: 0,
      output_tokens: 5,
      reasoning_output_tokens: 0,
    },
  },
];

function play(turn: CodexTurn, lines: Array<Record<string, unknown>>): Frame[] {
  const out: Frame[] = [];
  for (const l of lines) out.push(...turn.push(l));
  return out;
}

test('CodexTurn: the recorded stream yields continuity, status, then one ok result', () => {
  const turn = new CodexTurn();
  const frames = play(turn, FIXTURE);
  expect(frames.map((f) => f.kind)).toEqual(['continuity', 'status', 'result']);
  expect(frames[0]).toEqual({
    kind: 'continuity',
    token: '01a01f62-3568-75d0-96c7-8a48a297b1ff',
    sessionId: '01a01f62-3568-75d0-96c7-8a48a297b1ff',
  });
  const result = frames[2];
  if (result.kind !== 'result') throw new Error('expected result frame');
  expect(result.ok).toBe(true);
  expect(result.text).toBe('pong');
  expect(result.usage).toEqual({
    codex: { input: 29536, output: 5, cache_read: 9984, cache_write: 0, cost_cents: 0 },
  });
  expect(turn.done).toBe(true);
  // The terminal already fired; process exit must not add a second result.
  expect(turn.finish(0, '')).toBeNull();
});

// BUGS L806, measured on sloth 2026-11-09: a resumed thread's turn.completed
// carried the thread total (input 24,903,877) while the turn's own requests
// summed 441,113 — the change since the previous turn's last token_count.
test('a resumed turn reports its usage since the thread total it started from', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-home-'));
  const thread = '01a1bba2-b010-7db2-aff7-1582f4ec543c';
  const dir = path.join(home, 'sessions', '2026', '11', '08');
  fs.mkdirSync(dir, { recursive: true });
  const count = (input: number, cached: number, output: number) => JSON.stringify({
    type: 'event_msg',
    payload: { type: 'token_count', info: {
      total_token_usage: { input_tokens: input, cached_input_tokens: cached, cache_write_input_tokens: 0, output_tokens: output },
      last_token_usage: { input_tokens: 1, cached_input_tokens: 0, cache_write_input_tokens: 0, output_tokens: 1 },
    } },
  });
  fs.writeFileSync(path.join(dir, `rollout-2026-11-08T14-49-24-${thread}.jsonl`),
    [count(24_329_717, 24_000_000, 90_000), count(24_462_764, 24_100_000, 91_000), '{"type":"event_msg","payload":{"type":"task_complete"}}', ''].join('\n'));
  const prior = threadTotal(home, thread);
  expect(prior?.input_tokens).toBe(24_462_764);
  expect(threadTotal(home, 'other-thread')).toBeUndefined();
  const frames = play(new CodexTurn(undefined, prior), [{
    type: 'turn.completed',
    usage: { input_tokens: 24_903_877, cached_input_tokens: 24_500_000, cache_write_input_tokens: 0, output_tokens: 92_500 },
  }]);
  const result = frames[0];
  if (result.kind !== 'result') throw new Error('expected result frame');
  expect(result.usage).toEqual({
    codex: { input: 441_113, output: 1_500, cache_read: 400_000, cache_write: 0, cost_cents: 0 },
  });
});

// The live <status> bug (spec 6/16): the pre-seam runtime parsed claude's raw
// message shape, which codex's {item:{text}} raw does not have — so codex
// streamed NO interim status.
test('codex interim text reaches the <status> pipeline', () => {
  const turn = new CodexTurn();
  const frames = turn.push({
    type: 'item.completed',
    item: { type: 'agent_message', text: 'plan.\n<status>reading files…</status>' },
  });
  expect(frames).toHaveLength(1);
  const f = frames[0];
  if (f.kind !== 'status') throw new Error('expected status frame');
  expect(statusBlocks(f.text)).toEqual(['reading files…']);
});

test('CodexTurn: a stream with no terminal event still produces exactly one result', () => {
  // Auth failure / crash: codex exits non-zero having printed nothing usable.
  const turn = new CodexTurn();
  expect(play(turn, [{ type: 'thread.started', thread_id: 'x' }])).toHaveLength(1);
  const tail = turn.finish(1, 'stream error: unauthorized\n');
  if (tail?.kind !== 'result') throw new Error('expected result frame');
  expect(tail.ok).toBe(false);
  expect(tail.text).toContain('unauthorized');
});

test('CodexTurn: turn.failed is an error result carrying the last text', () => {
  const turn = new CodexTurn();
  const frames = play(turn, [
    { type: 'item.completed', item: { type: 'agent_message', text: 'partial' } },
    { type: 'turn.failed' },
  ]);
  const result = frames.at(-1)!;
  if (result.kind !== 'result') throw new Error('expected result frame');
  expect(result.ok).toBe(false);
  expect(turn.done).toBe(true);
});

test('CodexTurn: turn.failed with no prior text carries the error message', () => {
  const turn = new CodexTurn();
  const frames = play(turn, [
    { type: 'turn.failed', error: { message: 'model overloaded' } },
  ]);
  const result = frames.at(-1)!;
  if (result.kind !== 'result') throw new Error('expected result frame');
  expect(result.ok).toBe(false);
  // Dropping the payload delivered an error with no diagnostic at all.
  expect(result.text).toBe('model overloaded');
});

test('tomlValue: strings, arrays and tables render as TOML, never JSON', () => {
  expect(tomlValue('socat')).toBe('"socat"');
  expect(tomlValue(['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock']))
    .toBe('["STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"]');
  // A JSON object (`"K":"V"`) would fail codex's TOML parse and silently
  // degrade to a literal string; the inline-table form is load-bearing.
  expect(tomlValue({ FOO: 'bar' })).toBe('{"FOO" = "bar"}');
});

test('codexArgs: renders turn.tools into -c overrides so tools cross the MCP socket', () => {
  const args = codexArgs({
    cwd: '/home/node',
    env: {},
    tools: {
      arizuko: {
        command: 'socat',
        args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'],
        alwaysLoad: true,
      },
    },
  });
  expect(args).toContain('mcp_servers.arizuko.command="socat"');
  expect(args).toContain('mcp_servers.arizuko.args=["STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"]');
  // Agent-authored ~/.codex/config.toml must never add servers (J14 class).
  expect(args).toContain('--ignore-user-config');
  // Prompt arrives on stdin, never argv.
  expect(args.at(-1)).toBe('-');
});

/** Fresh and resumed turns discover the group runbook and render one manual. */
test.each([undefined, '01a02008-eeb1-7221-b72e-53a67645fb0c'])(
  'codex instructions retain one automatic route with continuity %s',
  (continuity) => {
    const args = codexArgs({ cwd: '/home/node', env: {}, continuity });
    const fallback = args.filter((arg) =>
      arg.startsWith('project_doc_fallback_filenames='));
    expect(fallback).toEqual([
      'project_doc_fallback_filenames=["CLAUDE.md"]',
    ]);
    expect(args[args.indexOf(fallback[0]) - 1]).toBe('-c');
    expect(args).toContain('--ignore-user-config');
    const systemPrompt = buildSystemPrompt({
      prompt: 'QUESTION', groupFolder: 'trading', chatJid: 'chat',
    }, codexHarness.profile, () => 'RESIDENT-MANUAL');
    expect(codexPrompt({ prompt: 'QUESTION', systemPrompt }, ''))
      .toBe('RESIDENT-MANUAL\n\nQUESTION');
  },
);

// Measured 2026-08-21: without this flag the hooks in $CODEX_HOME/hooks.json
// do not run and codex prints no warning at all, so the turn looks healthy with
// every gate absent. Pin it — a silent removal is indistinguishable from a pass.
test('codexArgs: the hook-trust bypass is present, or every policy is silently off', () => {
  expect(codexArgs({ cwd: '/home/node', env: {} })).toContain('--dangerously-bypass-hook-trust');
});

test('codexArgs: model passes through verbatim only when set', () => {
  expect(codexArgs({ cwd: '/home/node', env: {}, model: 'gpt-5.5' })).toContain('gpt-5.5');
  expect(codexArgs({ cwd: '/home/node', env: {} })).not.toContain('-m');
});

test('codexArgs: forwards effort for fresh and resumed runs', () => {
  for (const continuity of [undefined, 'session']) {
    const args = codexArgs({ cwd: '/home/node', env: {}, continuity, effort: 'high' });
    const index = args.indexOf('model_reasoning_effort="high"');
    expect(index).toBeGreaterThan(0);
    expect(args[index - 1]).toBe('-c');
  }
  expect(codexArgs({ cwd: '/home/node', env: {} }).join(' ')).not.toContain('model_reasoning_effort');
});

test('codexPrompt: system text and skills index precede the untouched envelope', () => {
  expect(codexPrompt({ prompt: '<messages>…</messages>', systemPrompt: 'SYSTEM' }, '<skills>idx</skills>'))
    .toBe('SYSTEM\n\n<skills>idx</skills>\n\n<messages>…</messages>');
  // No rendered system text → codex keeps its own default (the claude_code
  // preset is claude wiring and never crosses the seam).
  expect(codexPrompt({ prompt: 'p' }, '')).toBe('p');
});

// Layer 1 for codex. Verified against codex-cli 0.147.0 on 2026-08-20: a
// persisted turn returned a thread_id and `codex exec resume <id>` recalled a
// word given only in the earlier turn. `--ephemeral` writes no rollout.
test('codexArgs: no continuity keeps the fresh-session shape', () => {
  const args = codexArgs({ cwd: '/home/node', env: {} });
  expect(args[0]).toBe('exec');
  expect(args).not.toContain('resume');
  expect(args).not.toContain('--ephemeral');
  expect(args.slice(args.indexOf('--cd'))).toEqual(['--cd', '/home/node', '-']);
});

test('codexArgs: a continuity token becomes `exec resume <id>` and drops --cd', () => {
  const id = '01a02008-eeb1-7221-b72e-53a67645fb0c';
  const args = codexArgs({ cwd: '/home/node', env: {}, continuity: id });
  expect(args.slice(0, 2)).toEqual(['exec', 'resume']);
  // `codex exec resume` rejects --cd outright ("unexpected argument '--cd'
  // found") — passing it fails every resumed turn at argv parse.
  expect(args).not.toContain('--cd');
  expect(args.slice(-2)).toEqual([id, '-']);
});

test('codexArgs: mcp overrides survive a resumed turn', () => {
  const args = codexArgs({
    cwd: '/home/node',
    env: {},
    continuity: '01a02008-eeb1-7221-b72e-53a67645fb0c',
    tools: { arizuko: { command: 'socat', args: ['-'] } },
  });
  expect(args).toContain('mcp_servers.arizuko.command="socat"');
});

// codex exits 0 and prints nothing on stdout when the rollout is gone, so the
// exit code cannot be the signal — only the stderr text can.
test('CodexTurn.finish: a missing rollout is retryFresh, never a reply', () => {
  const t = new CodexTurn();
  const ev = t.finish(0, 'Error: thread/resume: thread/resume failed: '
    + 'no rollout found for thread id 11111111-2222-4333-8444-555555555555 (code -32600)');
  if (ev?.kind !== 'result') throw new Error('expected result frame');
  // No fabricated subtype: the requirement (retry the turn fresh) is a seam
  // field, not a claude-shaped hole to pass through.
  expect(ev.retryFresh).toBe(true);
  // The diagnostic must not reach the user as the turn's answer.
  expect(ev.text).toBeUndefined();
});

// --- model gateway --------------------------------------------------------

// No gateway in env: argv is identical to an empty env, so today's shape is
// untouched whatever else the container env carries.
test('codexArgs: no gateway leaves argv identical to today', () => {
  const plain = codexArgs({ cwd: '/home/node', env: {} });
  expect(codexArgs({ cwd: '/home/node', env: { HOME: '/home/node', OPENAI_API_KEY: 'real' } })).toEqual(plain);
  expect(plain.join(' ')).not.toContain('model_provider');
});

// Measured 2026-09-04 with codex-cli 0.153.2: this provider block routes the
// turn through the gateway; `wire_api=chat` is rejected as "no longer
// supported". The key VALUE never enters argv — codex reads it by env name.
test('codexArgs: a gateway is a `gateway` provider on the responses wire, key by env name', () => {
  const args = codexArgs({ cwd: '/home/node', env: { MODEL_GATEWAY_URL: 'http://gw:4000', MODEL_GATEWAY_KEY: 'virtual-key-abc123' } });
  expect(args).toContain('model_provider="gateway"');
  expect(args).toContain('model_providers.gateway.name="gateway"');
  expect(args).toContain('model_providers.gateway.base_url="http://gw:4000/v1"');
  expect(args).toContain('model_providers.gateway.wire_api="responses"');
  expect(args).toContain('model_providers.gateway.env_key="MODEL_GATEWAY_KEY"');
  expect(args).not.toContain('model_providers.gateway.requires_openai_auth=true');
  expect(args.join(' ')).not.toContain('virtual-key-abc123');
  expect(args.at(-1)).toBe('-');
});

// URL without a key: codex sends its own ChatGPT bearer to base_url instead
// (measured 2026-09-04), so requires_openai_auth replaces env_key.
test('codexArgs: a gateway without a key sends codex\'s own bearer', () => {
  const args = codexArgs({ cwd: '/home/node', env: { MODEL_GATEWAY_URL: 'http://gw:4000' } });
  expect(args).toContain('model_providers.gateway.requires_openai_auth=true');
  expect(args.join(' ')).not.toContain('env_key');
});

// Recorded 2026-11-08, codex-cli 0.167.0, HTTPS_PROXY answering every CONNECT 403:
// after the 5 stream retries and the websocket fallback codex loops on
// "waiting for network" until killed (BUGS L717).
test('CodexTurn: a refused model host ends the turn with a failure naming the host', () => {
  const turn = new CodexTurn('api.openai.com or chatgpt.com');
  const waiting = { type: 'error', message: 'Reconnecting... waiting for network (Connection failed: error sending request)' };
  const frames = play(turn, [
    { type: 'thread.started', thread_id: 't1' },
    { type: 'turn.started' },
    { type: 'error', message: 'Reconnecting... 5/5 (stream disconnected before completion: URL error: Proxy connection failed: HTTP CONNECT failed with status 403)' },
    waiting, waiting, waiting,
  ]);
  const result = frames.at(-1) as Extract<Frame, { kind: 'result' }>;
  expect(result.ok).toBe(false);
  expect(result.text).toContain('api.openai.com');
  expect(turn.done).toBe(true);
});

test('CodexTurn: any event between waiting events resets the count', () => {
  const turn = new CodexTurn();
  const waiting = { type: 'error', message: 'Reconnecting... waiting for network (x)' };
  const frames = play(turn, [
    waiting, waiting,
    { type: 'item.completed', item: { id: 'i', type: 'command_execution', command: 'ls' } },
    waiting, waiting,
  ]);
  expect(turn.done).toBe(false);
  expect(frames.some((f) => f.kind === 'result')).toBe(false);
});

/** A running Codex thread receives the deadline prompt in one resumed run. */
test('runCodexStream resumes the interrupted thread once with the deadline note', async () => {
  const { runCodexStream } = await import('./codex.js');
  const { DEADLINE_TEXT } = await import('./spool.js');
  const calls: import('./backend.js').Turn[] = [];
  const deadline = Date.now() + 60_030;
  const frames: Frame[] = [];
  for await (const frame of runCodexStream({ prompt: 'work', cwd: '/tmp', turnID: 't', env: {}, deadline }, async function* (turn) {
    calls.push(turn);
    if (calls.length === 1) {
      yield { kind: 'continuity', token: 'thread', sessionId: 'thread' };
      await new Promise<void>((resolve) => turn.signal!.addEventListener('abort', () => resolve(), { once: true }));
      yield { kind: 'result', ok: false, text: 'interrupted' };
    } else {
      yield { kind: 'result', ok: true, text: 'posted' };
    }
  })) frames.push(frame);
  expect(calls).toHaveLength(2);
  expect(calls[1].continuity).toBe('thread');
  expect(calls[1].prompt).toBe(DEADLINE_TEXT);
  expect(calls[1].deadline).toBe(deadline);
  expect(frames.filter((f) => f.kind === 'result')).toEqual([{ kind: 'result', ok: true, text: 'posted' }]);
});

/** A completed Codex turn does not start a deadline continuation. */
test('runCodexStream leaves completed turns alone', async () => {
  const { runCodexStream } = await import('./codex.js');
  let calls = 0;
  const frames: Frame[] = [];
  for await (const frame of runCodexStream({ prompt: 'work', cwd: '/tmp', turnID: 't', env: {}, deadline: Date.now() + 60_030 }, async function* () {
    calls++;
    yield { kind: 'continuity', token: 'thread', sessionId: 'thread' };
    yield { kind: 'result', ok: true, text: 'done' };
  })) frames.push(frame);
  expect(calls).toBe(1);
  expect(frames.at(-1)).toEqual({ kind: 'result', ok: true, text: 'done' });
});

/** A thread ID arriving after the warning still permits the continuation. */
test('runCodexStream interrupts when the thread ID arrives after the warning', async () => {
  const { runCodexStream } = await import('./codex.js');
  let calls = 0;
  for await (const _frame of runCodexStream({ prompt: 'work', cwd: '/tmp', turnID: 't', env: {}, deadline: Date.now() + 59_000 }, async function* (turn) {
    calls++;
    if (calls === 1) {
      await new Promise((resolve) => setTimeout(resolve, 10));
      yield { kind: 'continuity', token: 'late', sessionId: 'late' };
      expect(turn.signal?.aborted).toBe(true);
      yield { kind: 'result', ok: false };
    } else {
      expect(turn.continuity).toBe('late');
      yield { kind: 'result', ok: true };
    }
  })) {}
  expect(calls).toBe(2);
});

/** External cancellation does not start a deadline continuation. */
test('runCodexStream respects external cancellation', async () => {
  const { runCodexStream } = await import('./codex.js');
  const controller = new AbortController();
  let calls = 0;
  for await (const _frame of runCodexStream({ prompt: 'work', cwd: '/tmp', turnID: 't', env: {}, signal: controller.signal, deadline: Date.now() + 60_030 }, async function* () {
    calls++;
    yield { kind: 'continuity', token: 'thread', sessionId: 'thread' };
    controller.abort();
    yield { kind: 'result', ok: false };
  })) {}
  expect(calls).toBe(1);
});

/** The Codex process stops and a second process resumes its thread with the deadline prompt. */
test('codexHarness stops the process and resumes the same thread', async () => {
  const fs = await import('fs');
  const os = await import('os');
  const path = await import('path');
  const { DEADLINE_TEXT } = await import('./spool.js');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-test-'));
  const capture = path.join(dir, 'capture');
  fs.writeFileSync(path.join(dir, 'codex'), `#!/bin/bash
if [ "$2" = resume ]; then
  printf '%s\\n' "$@" > "$CAPTURE"
  cat >> "$CAPTURE"
  printf '%s\\n' '{"type":"turn.completed"}'
else
  printf '%s\\n' '{"type":"thread.started","thread_id":"thread-123"}'
  while :; do read -r -t 1 ignored || :; done
fi
`, { mode: 0o755 });
  const frames: Frame[] = [];
  for await (const frame of codexHarness.run({ cwd: dir, prompt: 'work', systemPrompt: 'original system', turnID: 't', env: { PATH: `${dir}:/usr/bin:/bin`, CAPTURE: capture }, deadline: Date.now() + 60_200 })) frames.push(frame);
  const captured = fs.readFileSync(capture, 'utf8');
  expect(captured).toContain('resume\n');
  expect(captured).toContain('thread-123\n');
  expect(captured.endsWith(DEADLINE_TEXT)).toBe(true);
  expect(captured).not.toContain('original system');
  expect(frames.filter((f) => f.kind === 'result')).toEqual([{ kind: 'result', ok: true, text: undefined, usage: undefined }]);
});

/** Cancellation before a run starts does not launch Codex. */
test('codexHarness does not start after cancellation', async () => {
  const controller = new AbortController();
  controller.abort();
  const frames: Frame[] = [];
  for await (const frame of codexHarness.run({ cwd: '/tmp', prompt: 'work', turnID: 't', env: {}, signal: controller.signal, deadline: Date.now() + 60_000 })) frames.push(frame);
  expect(frames).toEqual([]);
});

/** A stream without a thread ID cannot resume and retains its failure. */
test('runCodexStream preserves failure without a thread ID', async () => {
  const { runCodexStream } = await import('./codex.js');
  let calls = 0;
  const frames: Frame[] = [];
  for await (const frame of runCodexStream({ prompt: 'work', cwd: '/tmp', turnID: 't', env: {}, deadline: Date.now() + 59_000 }, async function* () {
    calls++;
    await new Promise((resolve) => setTimeout(resolve, 10));
    yield { kind: 'result', ok: false, text: 'no thread' };
  })) frames.push(frame);
  expect(calls).toBe(1);
  expect(frames).toEqual([{ kind: 'result', ok: false, text: 'no thread' }]);
});
