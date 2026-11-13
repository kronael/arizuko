// The chassis <status> pipeline: statusBlocks is THE one extraction site, so a
// harness only has to put its assistant text on the frame.

import { describe, test, expect } from 'bun:test';
import { DEADLINE_TEXT, buildSystemPrompt, checkIpcMessage, runTurn, statusBlocks } from './index.js';
import type { Frame, Harness } from './backend.js';
import type { SubmitStatusPayload } from './mcp.js';
import { NUDGE_TEXT, STEER_SPOOL, appendLines, drainLines } from './spool.js';

test('statusBlocks: extracts each trimmed block in order', () => {
  expect(statusBlocks('a <status>reading files…</status> b <status> two </status>'))
    .toEqual(['reading files…', 'two']);
});

test('statusBlocks: empty blocks and tagless text yield nothing', () => {
  expect(statusBlocks('<status>  </status>')).toEqual([]);
  expect(statusBlocks('no tags here')).toEqual([]);
});

test('statusBlocks: multiline block content survives', () => {
  expect(statusBlocks('<status>line one\nline two</status>')).toEqual(['line one\nline two']);
});

// The chassis side of the seam contract: exactly one terminal per turn, never
// silence. These drive runTurn with a scripted harness and a spy in place of
// submit_turn.
function scripted(frames: Frame[]): Harness {
  return {
    name: 'scripted',
    profile: { continuity: 'none', tools: 'none', guard: 'none', steering: 'between-turn', manual: 'native', status: 'streaming' },
    run: async function* () { yield* frames; },
  };
}

interface Delivered {
  status: string;
  error?: string;
  result?: string | null;
  models?: unknown;
  timedOut?: boolean;
  newSessionId?: string;
}

// statusSpy stands in for submit_status, the interim-⏳ half of the turn
// contract (spec 5/24).
function statusSpy() {
  const sent: string[] = [];
  return { sent, send: async (p: SubmitStatusPayload) => { if (p.text) sent.push(p.text); return { remind: false }; } };
}

function spy(): { deliveries: Delivered[]; deliver: (turnID: string, out: Delivered) => Promise<void> } {
  const deliveries: Delivered[] = [];
  return { deliveries, deliver: async (_turnID, out) => { deliveries.push(out); } };
}

const INPUT = { prompt: 'p', groupFolder: 'g', chatJid: 'j' };

/** Every model status is a plain progress text: ant sends no opener kind. */
test('runTurn sends each model status as turn_id and text only', async () => {
  const sent: SubmitStatusPayload[] = [];
  await runTurn(scripted([
    { kind: 'status', text: '<status>verify the release</status>' },
    { kind: 'status', text: '<status>running the tests</status>' },
    { kind: 'result', ok: true },
  ]), 'p', undefined, INPUT, { ARIZUKO_PROGRESS: '1' }, 'turn-1', async () => {}, async p => { if (p.text) sent.push(p); return { remind: false }; });
  expect(sent).toEqual([
    { turn_id: 'turn-1', text: 'verify the release' },
    { turn_id: 'turn-1', text: 'running the tests' },
  ]);
});

test('runTurn: retryFresh on a resumed turn is not delivered — the caller retries', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, retryFresh: true }]),
    'p', 'tok-1', INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(r.retryFresh).toBe(true);
  expect(deliveries).toEqual([]);
});

// claude maps EVERY error_during_execution to retryFresh, resume miss or not.
// On an already-fresh turn there is nothing to retry, and suppressing the
// frame too was a silent-turn path: no reply, "input empty, exiting".
test('runTurn: retryFresh on an already-fresh turn delivers an error, never silence', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, retryFresh: true }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(r.retryFresh).toBe(false);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
});

// A harness whose stream ends with no result frame at all (a reducer hole, an
// SDK terminal state with nothing to say) still owes the user a reply.
test('runTurn: a harness that yields no result frame still delivers an error turn', async () => {
  const { deliveries, deliver } = spy();
  await runTurn(scripted([{ kind: 'continuity', token: 't', sessionId: 's' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
  expect(deliveries[0].error).toContain('no result');
});

test('runTurn: a normal result is delivered exactly once', async () => {
  const { deliveries, deliver } = spy();
  await runTurn(scripted([{ kind: 'result', ok: true, text: 'done' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('success');
});

// Steering drained from IPC into the spool is delivered by the steering policy
// only when a tool call follows. A turn that stopped calling tools stranded those
// messages: the loop read only the IPC dir, saw nothing, and exited.
test('checkIpcMessage: leftover steering becomes the next turn prompt', () => {
  drainLines(STEER_SPOOL);
  appendLines(STEER_SPOOL, ['left over']);
  expect(checkIpcMessage()).toBe('left over');
  expect(checkIpcMessage()).toBeNull();
});

// A nudge steers a RUNNING turn; one stranded in the spool when the turn ends
// must never start a new one (the pre-spool discardNudges rule, kept).
test('checkIpcMessage: a stale progress nudge never starts a turn', () => {
  drainLines(STEER_SPOOL);
  appendLines(STEER_SPOOL, [NUDGE_TEXT]);
  expect(checkIpcMessage()).toBeNull();
});


// The <status> pipeline failed silently once already: the pre-seam runtime
// parsed claude's raw message shape, so codex streamed no interim status.
test('runTurn: repeated statuses reach the reducer in order', async () => {
  const d = spy();
  const st = statusSpy();
  await runTurn(
    scripted([
      { kind: 'status', text: 'thinking\n<status>reading files</status>' },
      { kind: 'status', text: '<status>reading files</status><status>writing</status>' },
      { kind: 'status', text: '<status>reading files</status>' },
      { kind: 'result', ok: true, text: 'done' },
    ]),
    'p', undefined, INPUT, { ARIZUKO_PROGRESS: '1' }, 't1', d.deliver, st.send,
  );
  expect(st.sent).toEqual(['reading files', 'reading files', 'writing', 'reading files']);
  expect(d.deliveries).toHaveLength(1);
});

test('runTurn: text with no status block sends nothing', async () => {
  const st = statusSpy();
  await runTurn(
    scripted([{ kind: 'status', text: 'just prose' }, { kind: 'result', ok: true, text: 'done' }]),
    'p', undefined, INPUT, { ARIZUKO_PROGRESS: '1' }, 't1', spy().deliver, st.send,
  );
  expect(st.sent).toEqual([]);
});

// continuity is opaque to the chassis — it must come back verbatim for the next
// turn, with sessionId carried separately because topic fork copies the
// transcript file named by it (the L54 class).
test('runTurn: the continuity token and session id come back untouched', async () => {
  const out = await runTurn(
    scripted([
      { kind: 'continuity', token: 'opaque#42', sessionId: 'sess-abc' },
      { kind: 'result', ok: true, text: 'done' },
    ]),
    'p', undefined, INPUT, {}, 't1', spy().deliver, statusSpy().send,
  );
  expect(out.token).toBe('opaque#42');
  expect(out.sessionId).toBe('sess-abc');
});

test('runTurn: a timed-out turn is delivered as a real reply, flagged', async () => {
  const d = spy();
  await runTurn(
    scripted([{ kind: 'result', ok: true, text: 'ran out of time', timedOut: true }]),
    'p', undefined, INPUT, {}, 't1', d.deliver, statusSpy().send,
  );
  expect(d.deliveries).toHaveLength(1);
  expect(d.deliveries[0].status).toBe('success');
  expect(d.deliveries[0].timedOut).toBe(true);
});

test('runTurn: per-model usage reaches the delivered turn — cost is not lost', async () => {
  const d = spy();
  const usage = { 'claude-opus-4-8': { input_tokens: 10, output_tokens: 2, cost_cents: 1 } } as never;
  await runTurn(
    scripted([{ kind: 'result', ok: true, text: 'done', usage }]),
    'p', undefined, INPUT, {}, 't1', d.deliver, statusSpy().send,
  );
  expect(d.deliveries[0].models).toEqual(usage);
});

test('runTurn: a failed result is delivered as an error, not dropped', async () => {
  const d = spy();
  await runTurn(
    scripted([{ kind: 'result', ok: false, text: 'the model refused' }]),
    'p', undefined, INPUT, {}, 't1', d.deliver, statusSpy().send,
  );
  expect(d.deliveries).toHaveLength(1);
  expect(d.deliveries[0].status).toBe('error');
});

describe('buildSystemPrompt layering', () => {
  // SYSTEM.md replaces the base prompt and PERSONA.md appends to it. runner.go
  // reads both and ships them on one wire; a dropped persona is invisible —
  // the turn still runs, just without the group's voice.
  const native = { manual: 'native' as const };
  const rendered = { manual: 'rendered' as const };
  const manual = () => 'THE-MANUAL';
  const noManual = () => null;

  test('persona follows systemMd in the rendered prompt', () => {
    const out = buildSystemPrompt({ systemMd: 'THE-SYSTEM', persona: 'THE-PERSONA' }, native, noManual);
    expect(out).toContain('THE-SYSTEM');
    expect(out).toContain('THE-PERSONA');
    expect(out!.indexOf('THE-SYSTEM')).toBeLessThan(out!.indexOf('THE-PERSONA'));
  });

  test('persona alone still reaches the prompt', () => {
    expect(buildSystemPrompt({ persona: 'ONLY-PERSONA' }, native, noManual)).toContain('ONLY-PERSONA');
  });

  test('neither set renders nothing the harness must override', () => {
    expect(buildSystemPrompt({}, native, noManual)).toBeUndefined();
  });

  // BUGS L130: a codex or pi agent received none of ~/.claude/CLAUDE.md and
  // failed every task that needed platform knowledge. The manual is one file;
  // a harness that reads it natively must not get it twice, and one that
  // cannot must get it between SYSTEM.md and the persona.
  test('a rendered-manual harness gets the manual after systemMd and before persona', () => {
    const out = buildSystemPrompt({ systemMd: 'THE-SYSTEM', persona: 'THE-PERSONA' }, rendered, manual)!;
    expect(out.indexOf('THE-SYSTEM')).toBeLessThan(out.indexOf('THE-MANUAL'));
    expect(out.indexOf('THE-MANUAL')).toBeLessThan(out.indexOf('THE-PERSONA'));
  });

  test('the manual alone is enough to render a system prompt', () => {
    expect(buildSystemPrompt({}, rendered, manual)).toBe('THE-MANUAL');
  });

  test('a native-manual harness never receives the manual from the chassis', () => {
    expect(buildSystemPrompt({ systemMd: 'THE-SYSTEM' }, native, manual)).not.toContain('THE-MANUAL');
  });

  test('a missing manual file renders nothing rather than an empty layer', () => {
    expect(buildSystemPrompt({}, rendered, noManual)).toBeUndefined();
  });
});

// A failed turn's text is a diagnostic, not the answer. It must ride `error`,
// because routd delivers a non-empty `result` to the chat as the agent's reply
// — which is how "API Error: 400 …" reached users (BUGS L175).
test('runTurn: a failed turn sends its text as error, never as the reply', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, text: 'API Error: 400 fake says no' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
  expect(deliveries[0]).not.toHaveProperty('result');
  expect(deliveries[0].error).toBe('API Error: 400 fake says no');
  expect(r.failed).toBe('retryable');
});

// The exit code is what puts the turn on routd's existing failure path, and a
// terminal failure must not be re-spawned.
test('runTurn: a terminal failure is reported as terminal', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, terminal: true, text: 'spend limit reached' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries[0].error).toBe('spend limit reached');
  expect(r.failed).toBe('terminal');
});

/** A refused message fails its turn through the delivered refusal, and the process stays up, so the run never reaches routd's instance breaker (BUGS L831). */
test('runTurn: a refused result is delivered as a refusal without failing the run', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, refused: true, text: 'safeguards flagged this message' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('refused');
  expect(deliveries[0].error).toBe('safeguards flagged this message');
  expect(r.failed).toBeUndefined();
});

test('runTurn: a clean turn reports no failure and leaves prose off the wire', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: true, text: 'the answer' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(deliveries[0]).not.toHaveProperty('result');
  expect(deliveries[0].error).toBeUndefined();
  expect(r.failed).toBeUndefined();
});

// A terminal failure carries the harness's own words out. The journal entry routd
// writes is built from the stderr TAIL, so a reason printed three lines before the
// exit is outside it: 27 terminal turns on sloth said only "Turn failed (terminal)"
// while the log on disk said "You've hit your individual spend limit" (BUGS L397).
test('runTurn: a terminal failure carries its reason to the caller', async () => {
  const { deliveries, deliver } = spy();
  const why = "You've hit your individual spend limit";
  const r = await runTurn(scripted([{ kind: 'result', ok: false, terminal: true, text: why }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(r.failed).toBe('terminal');
  expect(r.failedReason).toBe(why);
  // The delivered error already carried it; this asserts the two agree, so the
  // journal and the chat cannot name different causes for one turn.
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].error).toBe(why);
});

// A retryable failure with no text must not invent one. The exit line stays bare
// rather than ending in a dangling colon.
test('runTurn: a failure with no text leaves failedReason unset', async () => {
  const { deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver, statusSpy().send);
  expect(r.failed).toBe('retryable');
  expect(r.failedReason).toBeUndefined();
});

/** The nudge round is a second harness run: it gets the SAME deadline as the first, not a fresh timeout. */
test('runTurn hands one deadline to the first run and the nudge round', async () => {
  const deadlines: number[] = [];
  const harness: Harness = {
    ...scripted([]),
    run: async function* (turn) {
      deadlines.push(turn.deadline);
      yield { kind: 'result', ok: true, text: 'done' };
    },
  };
  let delivered = 0;
  await runTurn(harness, 'p', undefined, INPUT, {}, 't1', async () => ({ nudge: ++delivered === 1 ? 'deliver' : '' }), statusSpy().send, 12345);
  expect(deadlines).toEqual([12345, 12345]);
});

// lasting is a harness whose one run takes `ms` before its result.
function lasting(ms: number): Harness {
  return {
    ...scripted([]),
    run: async function* () {
      await new Promise((r) => setTimeout(r, ms));
      yield { kind: 'result', ok: true, text: 'done' };
    },
  };
}

/**
 * A run still going five minutes before the process deadline puts the [deadline]
 * note on the steer spool, where the steering hook or the waiting poll reads it.
 */
test('runTurn: five minutes before the deadline the steer spool gets the [deadline] note', async () => {
  drainLines(STEER_SPOOL);
  await runTurn(lasting(300), 'p', undefined, INPUT, {}, 't1', spy().deliver, statusSpy().send, Date.now() + 300_100);
  const lines = drainLines(STEER_SPOOL);
  expect(lines).toHaveLength(1);
  expect(lines[0]).toStartWith('[deadline] About five minutes are left in this run.');
  expect(lines[0]).toContain('end the turn');
  expect(lines[0]).toContain('TaskStop');
});

/** A run that starts inside the last five minutes gets no note: the warning time has passed. */
test('runTurn: a run that starts less than five minutes before the deadline queues no note', async () => {
  drainLines(STEER_SPOOL);
  await runTurn(lasting(300), 'p', undefined, INPUT, {}, 't1', spy().deliver, statusSpy().send, Date.now() + 299_000);
  expect(drainLines(STEER_SPOOL)).toEqual([]);
});

/** Unlike a progress nudge, a note left in the spool starts the next round. */
test('checkIpcMessage: a stranded [deadline] note becomes the next prompt', () => {
  drainLines(STEER_SPOOL);
  appendLines(STEER_SPOOL, [DEADLINE_TEXT]);
  expect(checkIpcMessage()).toBe(DEADLINE_TEXT);
});

/** A submission nudge continues the same turn and latest token, before the next IPC turn. */
test('runTurn resumes a delivery nudge on the same turn and transcript', async () => {
  const turns: Array<{ id: string; prompt: string; continuity: string | undefined }> = [];
  const harness: Harness = {
    ...scripted([]),
    run: async function* (turn) {
      turns.push({ id: turn.turnID, prompt: turn.prompt, continuity: turn.continuity });
      if (turns.length === 1) yield { kind: 'continuity', token: 'opaque#latest', sessionId: 'session' };
      yield { kind: 'result', ok: true, text: 'private terminal prose' };
    },
  };
  const delivered: string[] = [];
  const result = await runTurn(harness, 'original', 'old-token', INPUT, {}, 'bound-turn', async (id, output) => {
    delivered.push(id);
    expect(output).not.toHaveProperty('result');
    return { nudge: delivered.length === 1 ? '[deliver] call reply' : '' };
  }, statusSpy().send);
  expect(turns).toEqual([
    { id: 'bound-turn', prompt: 'original', continuity: 'old-token' },
    { id: 'bound-turn', prompt: '[deliver] call reply', continuity: 'opaque#latest' },
  ]);
  expect(delivered).toEqual(['bound-turn', 'bound-turn']);
  expect(result.sessionId).toBe('session');
  expect(result.token).toBe('opaque#latest');
});

/**
 * routd records no submission it nudges, so the round it nudged rides the next
 * submission: the turn's record is the sum of both rounds, with the account
 * state of the later one, and names the harness.
 */
test('runTurn carries a nudged round usage into the turn record', async () => {
  const rounds = [
    { codex: { input: 100, output: 10, cache_read: 50, cache_write: 0, cost_cents: 0,
      credits_spent: 2.5, credits_balance: 97.5, window_used_percent: 100, plan_type: 'pro' } },
    { codex: { input: 30, output: 3, cache_read: 20, cache_write: 0, cost_cents: 0,
      credits_spent: 0.5, credits_balance: 97, window_used_percent: 100, plan_type: 'pro' } },
  ];
  let run = 0;
  const harness: Harness = {
    ...scripted([]),
    name: 'codex',
    run: async function* () { yield { kind: 'result', ok: true, text: 'done', usage: rounds[run++] }; },
  };
  const delivered: Array<{ harness?: string; models?: unknown }> = [];
  await runTurn(harness, 'p', undefined, INPUT, {}, 't1', async (_id, output) => {
    delivered.push({ harness: output.harness, models: output.models });
    return { nudge: delivered.length === 1 ? 'deliver' : '' };
  }, statusSpy().send);
  expect(delivered[1]).toEqual({ harness: 'codex', models: { codex: {
    input: 130, output: 13, cache_read: 70, cache_write: 0, cost_cents: 0,
    credits_spent: 3, credits_balance: 97, window_used_percent: 100, plan_type: 'pro',
  } } });
});

/** Silent turns use the same one-shot timer and spool. */
test('runTurn queues the reminder returned by routd on a silent turn', async () => {
  drainLines(STEER_SPOOL);
  let calls = 0;
  await runTurn(lasting(30), 'p', undefined, INPUT, {}, 't1', spy().deliver, async () => ({ remind: ++calls > 1, reminder_after_ms: 1 }));
  expect(calls).toBe(2);
  expect(drainLines(STEER_SPOOL)).toEqual([NUDGE_TEXT]);
});

/** A status acknowledgment arriving after the turn cannot queue a reminder. */
test('runTurn ignores a late timer acknowledgment', async () => {
  drainLines(STEER_SPOOL);
  let release!: (value: { remind: boolean }) => void;
  let calls = 0;
  const ack = new Promise<{ remind: boolean }>(resolve => { release = resolve; });
  await runTurn(lasting(30), 'p', undefined, INPUT, {}, 't1', spy().deliver, async () => {
    calls++;
    return calls === 1 ? { remind: false, reminder_after_ms: 1 } : ack;
  });
  expect(calls).toBe(2);
  release({ remind: true });
  await Promise.resolve();
  await Promise.resolve();
  expect(drainLines(STEER_SPOOL)).toEqual([]);
});

/** A timer reminder remains active across an internal delivery nudge. */
test('runTurn keeps its reminder across delivery nudge attempts', async () => {
  drainLines(STEER_SPOOL);
  let attempts = 0;
  let calls = 0;
  const harness: Harness = {
    ...scripted([]),
    run: async function* () {
      attempts++;
      if (attempts === 2) await new Promise(resolve => setTimeout(resolve, 30));
      yield { kind: 'result', ok: true, text: 'done' };
    },
  };
  await runTurn(harness, 'p', undefined, INPUT, {}, 't1', async () => ({ nudge: attempts === 1 ? 'continue' : '' }), async () => ({ remind: ++calls > 1, reminder_after_ms: 1 }));
  expect(attempts).toBe(2);
  expect(calls).toBe(2);
  expect(drainLines(STEER_SPOOL)).toEqual([NUDGE_TEXT]);
});

/** The progress flag gates fallback text while reminders and delivery remain active. */
test.each([['1', { ARIZUKO_PROGRESS: '1' }], ['empty', { ARIZUKO_PROGRESS: '' }], ['absent', {}]] as const)('fallback status with progress %s', async (flag, env) => {
  drainLines(STEER_SPOOL);
  const sent: SubmitStatusPayload[] = [];
  const d = spy();
  const harness: Harness = {
    ...scripted([]),
    run: async function* () {
      yield { kind: 'status', text: '<status>working</status>' };
      await new Promise(resolve => setTimeout(resolve, 30));
      yield { kind: 'result', ok: true };
    },
  };
  await runTurn(harness, 'p', undefined, INPUT, env, 't1', d.deliver, async payload => {
    sent.push(payload);
    return payload.text ? { remind: false } : sent.filter(item => !item.text).length === 1
      ? { remind: false, reminder_after_ms: 1 } : { remind: true };
  });
  expect(sent.filter(item => item.text)).toEqual(flag === '1' ? [{ turn_id: 't1', text: 'working' }] : []);
  expect(sent.filter(item => !item.text)).toHaveLength(2);
  expect(drainLines(STEER_SPOOL)).toEqual([NUDGE_TEXT]);
  expect(d.deliveries).toHaveLength(1);
});
