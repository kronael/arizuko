// pi backend mapping, tested against RECORDED `pi -p --mode json` streams
// (pi 0.51.6, captured 2026-08-20). Both live captures errored (the serve
// endpoints were down — itself a recorded failure shape); the success shape
// is constructed from pi-ai's published types (StopReason "stop", content
// text parts), the one part a fixture could not supply.

import { test, expect } from 'bun:test';
import { PiTurn, piArgs } from './pi.js';
import type { Event, SessionConfig } from './backend.js';

// Recorded verbatim (trimmed to the load-bearing fields): the codex-serve
// endpoint answering 404 — stopReason "error" with an errorMessage.
const ERR_ASSISTANT = {
  role: 'assistant',
  content: [],
  provider: 'codex-serve',
  model: 'gpt-5.2-codex',
  usage: {
    input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  },
  stopReason: 'error',
  errorMessage: '404 404 page not found\n',
};

const ERR_FIXTURE = [
  { type: 'session', version: 3, id: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c' },
  { type: 'agent_start' },
  { type: 'turn_start' },
  { type: 'message_end', message: { role: 'user', content: [{ type: 'text', text: 'ping' }] } },
  { type: 'message_end', message: ERR_ASSISTANT },
  { type: 'turn_end', message: ERR_ASSISTANT, toolResults: [] },
  { type: 'agent_end', messages: [ERR_ASSISTANT] },
];

// The success shape per pi-ai types: StopReason "stop", text content parts.
const OK_ASSISTANT = {
  role: 'assistant',
  content: [{ type: 'text', text: 'pong' }],
  provider: 'claude-serve',
  model: 'haiku',
  usage: {
    input: 12, output: 3, cacheRead: 4, cacheWrite: 1, totalTokens: 20,
    cost: { total: 0.0234 },
  },
  stopReason: 'stop',
};

function play(turn: PiTurn, lines: Array<Record<string, unknown>>): Event[] {
  const out: Event[] = [];
  for (const l of lines) out.push(...turn.push(l));
  return out;
}

test('PiTurn: the recorded error stream ends as one error result — even on exit 0', () => {
  const turn = new PiTurn();
  const events = play(turn, ERR_FIXTURE);
  // No assistant text ever streamed; only the session init surfaced.
  expect(events.map((e) => e.type)).toEqual(['system_init']);
  expect(events[0].sessionId).toBe('7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c');
  // pi exits 0 after a failed retry loop; the terminal must still be an error.
  const result = turn.finish(0, '');
  expect(result.status).toBe('error');
  expect(result.text).toContain('404');
});

test('PiTurn: a success stream yields assistant text and one success result with usage', () => {
  const turn = new PiTurn();
  const events = play(turn, [
    { type: 'session', id: 's-1' },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(events.map((e) => e.type)).toEqual(['system_init', 'assistant']);
  expect(events[1].text).toBe('pong');
  const result = turn.finish(0, '');
  expect(result.status).toBe('success');
  expect(result.text).toBe('pong');
  expect(result.models).toEqual({
    'claude-serve/haiku': { input: 12, output: 3, cache_read: 4, cache_write: 1, cost_cents: 2 },
  });
});

test('PiTurn: a retried attempt that finally succeeds delivers the last text', () => {
  // auto_retry: agent_end fires once PER ATTEMPT; the terminal is process
  // exit, so an error attempt followed by a success attempt is a success.
  const turn = new PiTurn();
  play(turn, [
    { type: 'message_end', message: ERR_ASSISTANT },
    { type: 'auto_retry_start', attempt: 1 },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(turn.finish(0, '').status).toBe('success');
});

test('PiTurn: non-zero exit is an error result carrying stderr', () => {
  const result = new PiTurn().finish(1, 'boom\n');
  expect(result.status).toBe('error');
  expect(result.text).toBe('boom');
});

test('piArgs: no session, no self-granted extensions, scanned skills only, @file prompt', () => {
  const cfg: SessionConfig = { prompt: 'p', model: 'claude-serve/sonnet', systemPrompt: 'SYS' };
  const args = piArgs(cfg, '/tmp/prompt.md', ['/home/node/.claude/skills/diary']);
  expect(args).toContain('--no-session');
  // ~/.pi extensions are agent-authored tool registrations (J14 class);
  // --no-skills drops pi's own discovery while explicit --skill still loads.
  expect(args).toContain('--no-extensions');
  expect(args).toContain('--no-skills');
  const i = args.indexOf('--skill');
  expect(args[i + 1]).toBe('/home/node/.claude/skills/diary');
  expect(args).toContain('claude-serve/sonnet');
  expect(args).toContain('SYS');
  // The envelope travels as an @file, never argv (kernel per-arg cap).
  expect(args.at(-1)).toBe('@/tmp/prompt.md');
});

test('piArgs: the claude_code preset is claude-specific — pi keeps its own default', () => {
  const cfg: SessionConfig = { prompt: 'p', systemPrompt: { type: 'preset', preset: 'claude_code' } };
  expect(piArgs(cfg, '/tmp/p.md', [])).not.toContain('--system-prompt');
});
