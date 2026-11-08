import { expect, test } from 'bun:test';
import { ClaudeTurn, runClaude } from './claude.js';
import { CodexTurn } from './codex.js';
import { runTurn } from './index.js';
import type { Frame, Harness } from './backend.js';
import type { SubmitTurnPayload } from './mcp.js';

const spend = {
  type: 'result', subtype: 'success', is_error: true,
  result: 'API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"Monthly spend limit reached"}}',
};

/** An SDK refusal followed by a failed result carries the provider reset time. */
test('Claude rejected rate-limit fixture', () => {
  const turn = new ClaudeTurn();
  turn.push({ type: 'rate_limit_event', rate_limit_info: {
    status: 'rejected', resetsAt: 1794142800,
  } });
  expect(turn.push({ type: 'result', subtype: 'error_during_execution' })[0])
    .toMatchObject({ ok: false, terminal: true, providerFailure: {
      reason: 'rate_limit', reset_at: '2026-11-08T13:00:00.000Z',
    } });
});

/** An attempt-ending SDK rejection classifies only known provider failures. */
test('SDK rejection preserves provider origin', async () => {
  for (const status of [401, 429, 503, 403, 400, 0]) {
    const query = async function* () {
      throw Object.assign(new Error('request failed'), { status,
        error: { error: { type: 'api_error', message: 'request failed' } } });
    };
    const frames: Frame[] = [];
    try {
      for await (const frame of runClaude({ prompt: 'p', cwd: '/tmp', turnID: 't',
        env: {}, deadline: Date.now() + 10000 }, query)) frames.push(frame);
    } catch {
      expect([403, 400, 0]).toContain(status);
      continue;
    }
    expect(frames[0]).toMatchObject({ kind: 'result', ok: false,
      providerFailure: { reason: status === 401 ? 'authentication' : status === 429 ? 'rate_limit' : 'outage' } });
  }
});

/** A spend envelope follows the terminal exit-78 path and reaches submit_turn. */
test('spend envelope reaches the turn payload', async () => {
  const frames = new ClaudeTurn().push(spend);
  const harness: Harness = {
    name: 'fixture',
    profile: { continuity: 'none', tools: 'none', guard: 'none',
      steering: 'between-turn', manual: 'native', status: 'terminal-only' },
    run: async function* () { yield* frames; },
  };
  const results: Array<{ providerFailure?: SubmitTurnPayload['provider_failure'] }> = [];
  const result = await runTurn(harness, 'p', undefined,
    { prompt: 'p', groupFolder: 'g', chatJid: 'j' }, {}, 't',
    async (_id, output) => { results.push(output); }, async () => ({ remind: false }));
  expect(result.failed).toBe('terminal');
  expect(results).toHaveLength(1);
  expect(results[0]?.providerFailure).toEqual({ reason: 'spend_limit' });
});

/** A structured Codex error survives turn.failed and stream exit. */
test('Codex error event fixture', () => {
  for (const terminal of [true, false]) {
    const turn = new CodexTurn();
    turn.push({ type: 'error', code: 'insufficient_quota', message: 'Quota exceeded' });
    const result = terminal
      ? turn.push({ type: 'turn.failed', error: { message: 'Quota exceeded' } })[0]
      : turn.finish(1, '');
    expect(result).toMatchObject({ ok: false, providerFailure: { reason: 'spend_limit' } });
  }
  const turn = new CodexTurn();
  turn.push({ type: 'error', message: 'unexpected status 429 Too Many Requests: {"error":{"code":"rate_limit_exceeded","message":"Rate limit reached"}}' });
  expect(turn.push({ type: 'turn.failed', error: { message: 'request failed' } })[0])
    .toMatchObject({ providerFailure: { reason: 'rate_limit' } });
});

/** Content errors, assistant quotations, tool errors and unknown errors stay per-turn. */
test('provider lookalikes never pause', () => {
  const turn = new ClaudeTurn();
  expect(turn.push({ ...spend,
    result: 'API Error: 400 {"error":{"type":"invalid_request_error","message":"Bad content"}}',
  })[0]).toMatchObject({ terminal: true, providerFailure: undefined });
  for (const message of [
    { type: 'assistant', message: { content: [{ type: 'text', text: spend.result }] } },
    { type: 'user', message: { content: [{ type: 'tool_result', is_error: true, content: spend.result }] } },
    { type: 'rate_limit_event', rate_limit_info: { status: 'allowed_warning' } },
  ]) {
    expect(turn.push(message).every(frame => !('providerFailure' in frame))).toBe(true);
  }
  for (const result of ['Monthly spend limit reached', 'unknown error',
    'API Error: 403 {"error":{"message":"Forbidden"}}']) {
    expect(turn.push({ ...spend, result })[0]).toMatchObject({ providerFailure: undefined });
  }
  const codex = new CodexTurn();
  codex.push({ type: 'item.completed', item: { type: 'agent_message', text: spend.result } });
  codex.push({ type: 'error', message: 'unknown error' });
  const result: Frame | null = codex.finish(78, 'Monthly spend limit reached');
  expect(result).toMatchObject({ providerFailure: undefined });
});
