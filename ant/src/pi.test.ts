// pi harness mapping, tested against RECORDED `pi -p --mode json` streams (pi
// 0.51.6, captured 2026-08-20). Both live captures errored, so the success shape
// is constructed from pi-ai's published types.

import { test, expect } from 'bun:test';
import { PiTurn, piArgs } from './pi.js';
import type { Frame } from './backend.js';

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

function play(turn: PiTurn, lines: Array<Record<string, unknown>>): Frame[] {
  const out: Frame[] = [];
  for (const l of lines) out.push(...turn.push(l));
  return out;
}

test('PiTurn: the recorded error stream ends as one error result — even on exit 0', () => {
  const turn = new PiTurn();
  const frames = play(turn, ERR_FIXTURE);
  // No assistant text ever streamed; only the session init surfaced.
  expect(frames).toEqual([{
    kind: 'continuity',
    token: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c',
    sessionId: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c',
  }]);
  // pi exits 0 after a failed retry loop; the terminal must still be an error.
  const result = turn.finish(0, '');
  expect(result.ok).toBe(false);
  expect(result.text).toContain('404');
});

test('PiTurn: a success stream yields status text and one ok result with usage', () => {
  const turn = new PiTurn();
  const frames = play(turn, [
    { type: 'session', id: 's-1' },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(frames.map((f) => f.kind)).toEqual(['continuity', 'status']);
  const status = frames[1];
  if (status.kind !== 'status') throw new Error('expected status frame');
  expect(status.text).toBe('pong');
  const result = turn.finish(0, '');
  expect(result.ok).toBe(true);
  expect(result.text).toBe('pong');
  expect(result.usage).toEqual({
    'claude-serve/haiku': { input: 12, output: 3, cache_read: 4, cache_write: 1, cost_cents: 2 },
  });
});

// Each assistant message carries that one model call's usage, so a multi-message
// turn reports the SUM — keeping only the last dropped every earlier call, and
// per-message cent rounding zeroed a turn of sub-cent calls.
test('PiTurn: usage accumulates across assistant messages, rounded once', () => {
  const turn = new PiTurn();
  play(turn, [
    { type: 'message_end', message: OK_ASSISTANT },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(turn.finish(0, '').usage).toEqual({
    // 2 × (12, 3, 4, 1); cost 2 × 2.34 cents rounds to 5, not 2 × round(2.34).
    'claude-serve/haiku': { input: 24, output: 6, cache_read: 8, cache_write: 2, cost_cents: 5 },
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
  expect(turn.finish(0, '').ok).toBe(true);
});

test('PiTurn: non-zero exit is an error result carrying stderr', () => {
  const result = new PiTurn().finish(1, 'boom\n');
  expect(result.ok).toBe(false);
  expect(result.text).toBe('boom');
});

test('piArgs: no self-granted extensions, scanned skills only, @file prompt', () => {
  const args = piArgs(
    { cwd: '/home/node', model: 'claude-serve/sonnet', systemPrompt: 'SYS' },
    '/tmp/prompt.md',
    ['/home/node/.claude/skills/diary'],
  );
  expect(args).not.toContain('--no-session');
  // ~/.pi extensions are agent-authored tool registrations (J14 class);
  // --no-skills drops pi's own discovery while explicit --skill still loads.
  expect(args).toContain('--no-extensions');
  expect(args).toContain('--no-skills');
  // Discovery off, but an explicit -e still loads — that is how arizuko's
  // policy rows run on pi while nothing the agent wrote under ~/.pi does.
  expect(args[args.indexOf('-e') + 1]).toBe('/tmp/dist/pi-extension.js');
  const i = args.indexOf('--skill');
  expect(args[i + 1]).toBe('/home/node/.claude/skills/diary');
  expect(args).toContain('claude-serve/sonnet');
  expect(args).toContain('SYS');
  // The envelope travels as an @file, never argv (kernel per-arg cap).
  expect(args.at(-1)).toBe('@/tmp/prompt.md');
});

test('piArgs: no rendered system text keeps pi\'s own default', () => {
  // The claude_code preset is claude wiring and never crosses the seam; an
  // absent systemPrompt must not become a --system-prompt flag.
  expect(piArgs({ cwd: '/home/node' }, '/tmp/p.md', [])).not.toContain('--system-prompt');
});

test('piArgs: a fresh turn names the session dir but resumes nothing', () => {
  // --session-dir on every turn: on a fresh turn it is where the new rollout
  // lands, and on a resume it is what the bare id is resolved against.
  const args = piArgs({ cwd: '/home/node' }, '/tmp/p.md', []);
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/.pi/sessions');
  expect(args).not.toContain('--session');
});

test('piArgs: a stored id resumes against this group own session dir', () => {
  const args = piArgs(
    { cwd: '/home/node', continuity: 's-1' },
    '/tmp/p.md',
    [],
  );
  expect(args[args.indexOf('--session') + 1]).toBe('s-1');
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/.pi/sessions');
});

test('piArgs: the session dir follows the group home, never a fixed path', () => {
  // Every group gets its own dir, so one group id can never resolve inside
  // another group home.
  const args = piArgs({ cwd: '/home/node/other' }, '/tmp/p.md', []);
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/other/.pi/sessions');
});

test('PiTurn: a missing rollout retries fresh instead of delivering the diagnostic', () => {
  // pi prints this and exits 1 when the id resolves to no rollout in our dir
  // (main.js:331-333). Delivering it would reply to the user with a harness
  // error; the chassis must run the turn again with no continuity.
  const t = new PiTurn();
  const r = t.finish(1, "No session found matching 's-gone'");
  expect(r.ok).toBe(false);
  expect(r.retryFresh).toBe(true);
  expect(r.text).toBeUndefined();
});

test('PiTurn: an ordinary failure is delivered, not retried', () => {
  const t = new PiTurn();
  const r = t.finish(1, 'provider auth failed');
  expect(r.ok).toBe(false);
  expect(r.retryFresh).toBeUndefined();
  expect(r.text).toBe('provider auth failed');
});
