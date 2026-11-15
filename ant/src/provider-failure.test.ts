import { expect, test } from 'bun:test';
import { ClaudeTurn, runClaude } from './claude.js';
import { CodexTurn } from './codex.js';
import { PiTurn } from './pi.js';
import { runTurn } from './index.js';
import { isProviderFailurePermanent, providerFailure, type Frame, type Harness } from './backend.js';
import type { SubmitTurnPayload } from './mcp.js';

const spend = {
  type: 'result', subtype: 'success', is_error: true,
  result: 'API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"Monthly spend limit reached"}}',
};

/** L879: a rejected rate limit must permit replay and retain its reset time. */
test('Claude rejected rate-limit fixture', () => {
  const turn = new ClaudeTurn();
  turn.push({ type: 'rate_limit_event', rate_limit_info: {
    status: 'rejected', resetsAt: 1794142800,
  } });
  expect(turn.push({ type: 'result', subtype: 'error_during_execution' })[0])
    .toMatchObject({ ok: false, terminal: false, providerFailure: {
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
      expect(status).toBe(0);
      continue;
    }
    expect(frames[0]).toMatchObject({ kind: 'result', ok: false, terminal: [400, 401, 403].includes(status),
      providerFailure: status === 401 ? { reason: 'authentication' } : status === 429 ? { reason: 'rate_limit' } : status === 503 ? { reason: 'outage' } : undefined });
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

/** Final diagnostics determine the verdict and ambiguous diagnostics stop. */
test('Claude final diagnostics take precedence over a rejected rate event', () => {
  const event = { type: 'rate_limit_event', rate_limit_info: { status: 'rejected', resetsAt: 2000000000 } };
  const turn = new ClaudeTurn();
  turn.push(event);
  expect(turn.push({ type: 'result', subtype: 'error_during_execution', errors: [
    'API Error: 401 {"error":{"message":"bad key"}}',
  ] })[0]).toMatchObject({ terminal: true, providerFailure: { reason: 'authentication' } });
  expect(turn.push({ type: 'result', subtype: 'error_during_execution', errors: [
    'API Error: 400 malformed',
  ] })[0]).toMatchObject({ terminal: true, providerFailure: undefined });
  expect(turn.push({ type: 'result', subtype: 'error_during_execution', errors: [
    'Monthly spend limit reached',
  ] })[0]).toMatchObject({ terminal: false, providerFailure: { reason: 'spend_limit', reset_at: '2033-05-18T03:33:20.000Z' } });
  expect(turn.push({ type: 'result', subtype: 'error_during_execution', errors: [
    'unknown', 'API Error: 429 {"error":{"message":"rate"}}',
  ] })[0]).toMatchObject({ terminal: undefined, providerFailure: undefined, text: 'unknown\nAPI Error: 429 {"error":{"message":"rate"}}' });
});

/** Only exact single SDK missing-target diagnostics permit a fresh retry. */
test('Claude repairs only an exact missing target', () => {
  const uuid = '12345678-1234-1234-1234-123456789abc';
  for (const diagnostic of [`No conversation found with session ID: ${uuid}`, `No message found with message.uuid of: ${uuid}`]) {
    expect(new ClaudeTurn().push({ type: 'result', subtype: 'error_during_execution', errors: [diagnostic] })[0]).toMatchObject({ retryFresh: true });
    expect(new ClaudeTurn().push({ type: 'result', subtype: 'error_during_execution', errors: [diagnostic, 'other'] })[0]).toMatchObject({ retryFresh: undefined });
    expect(new ClaudeTurn().push({ type: 'result', subtype: 'error_during_execution', errors: [`API Error: 400 {"error":{"message":"${diagnostic}"}}`] })[0]).toMatchObject({ retryFresh: undefined, terminal: true });
  }
  expect(new ClaudeTurn().push({ type: 'result', subtype: 'error_during_execution', errors: ['Failed to resume session'] })[0]).toMatchObject({ retryFresh: undefined });
});

/** The shared policy decides provider verdicts for both Codex completion paths. */
test('Codex provider verdict respects reason and reset', () => {
  for (const [code, reset, terminal] of [
    ['rate_limit_exceeded', undefined, false],
    ['invalid_api_key', undefined, true],
    ['insufficient_quota', undefined, true],
    ['insufficient_quota', '2099-01-01T00:00:00Z', false],
  ] as const) {
    for (const finish of [false, true]) {
      const turn = new CodexTurn();
      turn.push({ type: 'error', code, reset_at: reset, message: 'request failed' });
      const frame = finish ? turn.finish(1, '') : turn.push({ type: 'turn.failed' })[0];
      expect(frame).toMatchObject({ terminal });
    }
  }
});

/** Reset boundaries and reason rules come from the shared policy. */
test('provider policy covers reset boundaries', () => {
 const now = Date.parse('2026-01-01T00:00:00Z');
 for (const reset of [undefined, 'invalid', '2025-12-31T00:00:00Z', '2026-01-01T00:00:00Z'])
  expect(isProviderFailurePermanent({ reason: 'spend_limit', reset_at: reset }, now)).toBe(true);
 expect(isProviderFailurePermanent({ reason: 'spend_limit', reset_at: '2026-01-02T00:00:00Z' }, now)).toBe(false);
 expect(isProviderFailurePermanent({ reason: 'authentication', reset_at: '2026-01-02T00:00:00Z' }, now)).toBe(true);
 expect(isProviderFailurePermanent({ reason: 'outage' }, now)).toBe(false);
});

/** Thrown HTTP statuses use the same retry policy as result payloads. */
test('SDK thrown HTTP status selects verdict', async () => {
 for (const [status, terminal] of [[408, false], [425, false], [400, true]] as const) {
  for (const error of [Object.assign(new Error('request failed'), { status }), new Error(`API Error: ${status}`)]) {
   const query = async function* () { throw error; };
   const frames: Frame[] = [];
   for await (const frame of runClaude({ prompt: 'p', cwd: '/tmp', turnID: 't', env: {}, deadline: Date.now() + 10000 }, query)) frames.push(frame);
   expect(frames[0]).toMatchObject({ ok: false, terminal });
  }
 }
});

/** Known HTTP failures retain their verdict without repairing a resumed session. */
test('resumed HTTP failures bypass continuity repair', async () => {
  for (const status of [408, 425, 400]) {
    const frames = new ClaudeTurn().push({
      type: 'result', subtype: 'error_during_execution', errors: [`API Error: ${status}`],
    });
    const harness: Harness = {
      name: 'fixture',
      profile: { continuity: 'native', tools: 'none', guard: 'none',
        steering: 'between-turn', manual: 'native', status: 'terminal-only' },
      run: async function* () { yield* frames; },
    };
    let calls = 0;
    const result = await runTurn(harness, 'p', 'session',
      { prompt: 'p', groupFolder: 'g', chatJid: 'j' }, {}, 't',
      async () => { calls++; }, async () => ({ remind: false }));
    expect(result.retryFresh).toBe(false);
    expect(result.failed).toBe(status === 400 ? 'terminal' : 'retryable');
    expect(calls).toBe(1);
  }
});

/** Cancellation remains terminal even when the SDK exposes a transient status. */
test('SDK cancellation overrides transient facts', async () => {
  for (const status of [408, 429]) {
    const cause = Object.assign(new Error('canceled'), { name: 'AbortError' });
    const query = async function* () {
      throw Object.assign(new Error('request failed', { cause }), {
        status, error: { error: { type: 'api_error', message: 'request failed' } },
      });
    };
    const frames: Frame[] = [];
    for await (const frame of runClaude({ prompt: 'p', cwd: '/tmp', turnID: 't',
      env: {}, deadline: Date.now() + 10000 }, query)) frames.push(frame);
    expect(frames[0]).toMatchObject({ ok: false, terminal: true });
  }
});

const gateway = (status: number, message: string) => ({ status, message,
  body: `{"error":{"message":"${message}","type":"budget_exceeded","param":null,"code":"${status}"}}` });
const refusals = [429, 422].flatMap(status => [
  gateway(status, 'Budget has been exceeded! Team=corp/eng Current cost: 5.2, Max budget: 5.0'),
  gateway(status, 'ExceededBudget: User=u1 over budget. Spend=5.2, Budget=5.0'),
]);

/** A gateway budget refusal is one folder's or person's cap, never a provider failure. */
test('gateway budget refusal is not a provider failure', () => {
  for (const { status, message } of refusals) {
    expect(providerFailure(status, String(status), message)).toBeUndefined();
    expect(providerFailure(status, 'budget_exceeded', 'refused')).toBeUndefined();
  }
  expect(providerFailure(429, '429', 'Rate limit reached')).toEqual({ reason: 'rate_limit' });
});

const budget = { kind: 'result', ok: false, refused: 'budget' };
const expectBudget = (frame: unknown, text?: string) => {
  expect(frame).toMatchObject(text === undefined ? budget : { ...budget, text });
  expect(frame).not.toHaveProperty('providerFailure');
};

/** Claude delivers the refusal as an API Error result or as a thrown SDK error. */
test('Claude gateway budget refusal is a budget result', async () => {
  for (const { status, message, body } of refusals) {
    expectBudget(new ClaudeTurn().push({ ...spend, result: `API Error: ${status} ${body}` })[0], `API Error: ${status} ${body}`);
    for (const error of [
      Object.assign(new Error(`API Error: ${status} ${body}`), { status }),
      Object.assign(new Error(message), { status, error: { error: { type: 'budget_exceeded', message } } }),
    ]) {
      const query = async function* () { throw error; };
      const frames: Frame[] = [];
      for await (const frame of runClaude({ prompt: 'p', cwd: '/tmp', turnID: 't',
        env: {}, deadline: Date.now() + 10000 }, query)) frames.push(frame);
      expectBudget(frames[0], error.message);
    }
  }
});

/** Codex forwards the refusal inside its "unexpected status" message. */
test('Codex gateway budget refusal is a budget result', () => {
  for (const { status, body } of refusals) {
    const message = `unexpected status ${status} Too Many Requests: ${body}, url: http://gateway/v1/responses, request id: r1`;
    for (const finish of [false, true]) {
      const turn = new CodexTurn();
      turn.push({ type: 'error', message });
      const frame = finish ? turn.finish(1, '') : turn.push({ type: 'turn.failed', error: { message: 'request failed' } })[0];
      expectBudget(frame);
    }
  }
});

/** Pi reports the provider error as the errorMessage of a failed assistant message. */
test('Pi gateway budget refusal is a budget result', () => {
  for (const { status, message, body } of refusals) {
    for (const errorMessage of [`${status} ${body}`, `${status} ${message}`]) {
      const turn = new PiTurn();
      turn.push({ type: 'message_end', message: { role: 'assistant', content: [], stopReason: 'error', errorMessage } });
      expectBudget(turn.finish(0, ''), errorMessage);
    }
  }
});

/** Assistant text, tool output and stderr that quote the refusal stay ordinary failures. */
test('quoted budget refusals do not classify', () => {
  const quote = refusals[0].message;
  const turn = new ClaudeTurn();
  expect(turn.push({ type: 'assistant', message: { content: [{ type: 'text', text: quote }] } })
    .every(frame => frame.kind !== 'result')).toBe(true);
  expect(turn.push({ ...spend, result: quote })[0]).not.toHaveProperty('refused', 'budget');
  const codex = new CodexTurn();
  codex.push({ type: 'item.completed', item: { type: 'agent_message', text: quote } });
  expect(codex.finish(1, quote)).toMatchObject({ ok: false });
  expect(codex.finish(1, quote)).not.toHaveProperty('refused', 'budget');
  const pi = new PiTurn();
  pi.push({ type: 'message_end', message: { role: 'assistant', stopReason: 'stop', content: [{ type: 'text', text: quote }] } });
  expect(pi.finish(1, quote)).toMatchObject({ ok: false });
  expect(pi.finish(1, quote)).not.toHaveProperty('refused', 'budget');
});
