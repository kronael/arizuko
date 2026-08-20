// codex backend mapping, tested against the RECORDED `codex exec --json`
// stream (codex-cli 0.147.0, captured 2026-08-20 with a live one-turn run) —
// the event shapes are fixtures, not guesses.

import { test, expect } from 'bun:test';
import { CodexTurn, codexArgs, codexBackend, codexPrompt, tomlValue } from './codex.js';
import type { Event, SessionConfig } from './backend.js';

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

function play(turn: CodexTurn, lines: Array<Record<string, unknown>>): Event[] {
  const out: Event[] = [];
  for (const l of lines) out.push(...turn.push(l));
  return out;
}

test('CodexTurn: the recorded stream yields init, assistant, then one success result', () => {
  const turn = new CodexTurn();
  const events = play(turn, FIXTURE);
  expect(events.map((e) => e.type)).toEqual(['system_init', 'assistant', 'result']);
  expect(events[0].sessionId).toBe('01a01f62-3568-75d0-96c7-8a48a297b1ff');
  expect(events[1].text).toBe('pong');
  const result = events[2];
  expect(result.status).toBe('success');
  expect(result.text).toBe('pong');
  expect(result.final).toBe(true);
  expect(result.models).toEqual({
    codex: { input: 29536, output: 5, cache_read: 9984, cache_write: 0, cost_cents: 0 },
  });
  expect(turn.done).toBe(true);
  // The terminal already fired; process exit must not add a second result.
  expect(turn.finish(0, '')).toBeNull();
});

test('CodexTurn: a stream with no terminal event still produces exactly one result', () => {
  // Auth failure / crash: codex exits non-zero having printed nothing usable.
  const turn = new CodexTurn();
  expect(play(turn, [{ type: 'thread.started', thread_id: 'x' }])).toHaveLength(1);
  const tail = turn.finish(1, 'stream error: unauthorized\n');
  expect(tail?.status).toBe('error');
  expect(tail?.text).toContain('unauthorized');
});

test('CodexTurn: turn.failed is an error result carrying the last text', () => {
  const turn = new CodexTurn();
  const events = play(turn, [
    { type: 'item.completed', item: { type: 'agent_message', text: 'partial' } },
    { type: 'turn.failed' },
  ]);
  const result = events.at(-1)!;
  expect(result.type).toBe('result');
  expect(result.status).toBe('error');
  expect(turn.done).toBe(true);
});

test('tomlValue: strings, arrays and tables render as TOML, never JSON', () => {
  expect(tomlValue('socat')).toBe('"socat"');
  expect(tomlValue(['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock']))
    .toBe('["STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"]');
  // A JSON object (`"K":"V"`) would fail codex's TOML parse and silently
  // degrade to a literal string; the inline-table form is load-bearing.
  expect(tomlValue({ FOO: 'bar' })).toBe('{"FOO" = "bar"}');
});

test('codexArgs: renders cfg.mcpServers into -c overrides so tools cross the gated socket', () => {
  const cfg: SessionConfig = {
    prompt: 'hi',
    mcpServers: {
      arizuko: {
        command: 'socat',
        args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'],
        alwaysLoad: true,
      },
    },
  };
  const args = codexArgs(cfg);
  expect(args).toContain('mcp_servers.arizuko.command="socat"');
  expect(args).toContain('mcp_servers.arizuko.args=["STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"]');
  // Agent-authored ~/.codex/config.toml must never add servers (J14 class).
  expect(args).toContain('--ignore-user-config');
  // Prompt arrives on stdin, never argv.
  expect(args.at(-1)).toBe('-');
});

test('codexArgs: model passes through verbatim only when set', () => {
  expect(codexArgs({ prompt: 'p', model: 'gpt-5.5' })).toContain('gpt-5.5');
  expect(codexArgs({ prompt: 'p' })).not.toContain('-m');
});

test('codexPrompt: system text and skills index precede the untouched envelope', () => {
  const cfg: SessionConfig = { prompt: '<messages>…</messages>', systemPrompt: 'SYSTEM' };
  expect(codexPrompt(cfg, '<skills>idx</skills>'))
    .toBe('SYSTEM\n\n<skills>idx</skills>\n\n<messages>…</messages>');
  // The claude_code preset is claude-specific: codex gets no system text.
  const preset: SessionConfig = {
    prompt: 'p',
    systemPrompt: { type: 'preset', preset: 'claude_code' },
  };
  expect(codexPrompt(preset, '')).toBe('p');
});

// Layer 1 for codex. Verified against codex-cli 0.147.0 on 2026-08-20: a
// persisted turn returned thread_id 01a02008-…, and `codex exec resume
// <id>` recalled a word given only in the earlier turn, reading 29,184
// cached input tokens. `--ephemeral` (which the connector used to pass
// unconditionally) writes no rollout, so there is nothing to resume.
test('codexArgs: no resume target keeps the fresh-session shape', () => {
  const args = codexArgs({ prompt: 'p', cwd: '/home/node' });
  expect(args[0]).toBe('exec');
  expect(args).not.toContain('resume');
  expect(args).not.toContain('--ephemeral');
  expect(args.slice(args.indexOf('--cd'))).toEqual(['--cd', '/home/node', '-']);
});

test('codexArgs: a resume target becomes `exec resume <id>` and drops --cd', () => {
  const id = '01a02008-eeb1-7221-b72e-53a67645fb0c';
  const args = codexArgs({ prompt: 'p', cwd: '/home/node', resume: id });
  expect(args.slice(0, 2)).toEqual(['exec', 'resume']);
  // `codex exec resume` rejects --cd outright ("unexpected argument '--cd'
  // found") — passing it fails every resumed turn at argv parse.
  expect(args).not.toContain('--cd');
  expect(args.slice(-2)).toEqual([id, '-']);
});

test('codexArgs: mcp overrides survive a resumed turn', () => {
  const args = codexArgs({
    prompt: 'p',
    resume: '01a02008-eeb1-7221-b72e-53a67645fb0c',
    mcpServers: { arizuko: { command: 'socat', args: ['-'] } },
  });
  expect(args).toContain('mcp_servers.arizuko.command="socat"');
});

// codex exits 0 and prints nothing on stdout when the rollout is gone, so the
// exit code cannot be the signal — only the stderr text can.
test('CodexTurn.finish: a missing rollout is a session error, never a reply', () => {
  const t = new CodexTurn();
  const ev = t.finish(0, 'Error: thread/resume: thread/resume failed: '
    + 'no rollout found for thread id 11111111-2222-4333-8444-555555555555 (code -32600)');
  expect(ev).not.toBeNull();
  expect((ev!.raw as { subtype?: string }).subtype).toBe('session_not_found');
  expect(codexBackend.isSessionError('session_not_found')).toBe(true);
  // The diagnostic must not reach the user as the turn's answer.
  expect(ev!.text).toBeUndefined();
});

test('codexBackend: resolveResume passes the stored id through', () => {
  const id = '01a02008-eeb1-7221-b72e-53a67645fb0c';
  expect(codexBackend.resolveResume(id)).toBe(id);
  expect(codexBackend.resolveResume(undefined)).toBeUndefined();
  expect(codexBackend.isSessionError('other')).toBe(false);
});
