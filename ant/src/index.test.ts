// The chassis <status> pipeline: statusBlocks is THE one extraction site, so a
// harness only has to put its assistant text on the frame.

import { describe, test, expect } from 'bun:test';
import { NUDGE_TEXT, buildSystemPrompt, checkIpcMessage, runTurn, statusBlocks } from './index.js';
import type { Frame, Harness } from './backend.js';
import { STEER_SPOOL, appendLines, drainLines } from './spool.js';

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
function statusSpy(): { sent: string[]; send: (p: { turn_id: string; text: string }) => Promise<void> } {
  const sent: string[] = [];
  return { sent, send: async (p) => { sent.push(p.text); } };
}

function spy(): { deliveries: Delivered[]; deliver: (turnID: string, out: Delivered) => Promise<void> } {
  const deliveries: Delivered[] = [];
  return { deliveries, deliver: async (_turnID, out) => { deliveries.push(out); } };
}

const INPUT = { prompt: 'p', groupFolder: 'g', chatJid: 'j' };

test('runTurn: retryFresh on a resumed turn is not delivered — the caller retries', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, retryFresh: true }]),
    'p', 'tok-1', INPUT, {}, 'turn-1', deliver);
  expect(r.retryFresh).toBe(true);
  expect(deliveries).toEqual([]);
});

// claude maps EVERY error_during_execution to retryFresh, resume miss or not.
// On an already-fresh turn there is nothing to retry, and suppressing the
// frame too was a silent-turn path: no reply, "input empty, exiting".
test('runTurn: retryFresh on an already-fresh turn delivers an error, never silence', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, retryFresh: true }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver);
  expect(r.retryFresh).toBe(false);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
});

// A harness whose stream ends with no result frame at all (a reducer hole, an
// SDK terminal state with nothing to say) still owes the user a reply.
test('runTurn: a harness that yields no result frame still delivers an error turn', async () => {
  const { deliveries, deliver } = spy();
  await runTurn(scripted([{ kind: 'continuity', token: 't', sessionId: 's' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
  expect(deliveries[0].error).toContain('no result');
});

test('runTurn: a normal result is delivered exactly once', async () => {
  const { deliveries, deliver } = spy();
  await runTurn(scripted([{ kind: 'result', ok: true, text: 'done' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver);
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
test('runTurn: a status frame reaches submit_status once, and repeats are dropped', async () => {
  const d = spy();
  const st = statusSpy();
  await runTurn(
    scripted([
      { kind: 'status', text: 'thinking\n<status>reading files</status>' },
      { kind: 'status', text: '<status>reading files</status><status>writing</status>' },
      { kind: 'result', ok: true, text: 'done' },
    ]),
    'p', undefined, INPUT, {}, 't1', d.deliver, st.send,
  );
  // Deduped across frames: the repeated block is sent once, in order.
  expect(st.sent).toEqual(['reading files', 'writing']);
  expect(d.deliveries).toHaveLength(1);
});

test('runTurn: text with no status block sends nothing', async () => {
  const st = statusSpy();
  await runTurn(
    scripted([{ kind: 'status', text: 'just prose' }, { kind: 'result', ok: true, text: 'done' }]),
    'p', undefined, INPUT, {}, 't1', spy().deliver, st.send,
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
    'p', undefined, INPUT, {}, 'turn-1', deliver);
  expect(deliveries).toHaveLength(1);
  expect(deliveries[0].status).toBe('error');
  expect(deliveries[0].result).toBeNull();
  expect(deliveries[0].error).toBe('API Error: 400 fake says no');
  expect(r.failed).toBe('retryable');
});

// The exit code is what puts the turn on routd's existing failure path, and a
// terminal failure must not be re-spawned.
test('runTurn: a terminal failure is reported as terminal', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: false, terminal: true, text: 'spend limit reached' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver);
  expect(deliveries[0].error).toBe('spend limit reached');
  expect(r.failed).toBe('terminal');
});

test('runTurn: a clean turn reports no failure and keeps its text as the reply', async () => {
  const { deliveries, deliver } = spy();
  const r = await runTurn(scripted([{ kind: 'result', ok: true, text: 'the answer' }]),
    'p', undefined, INPUT, {}, 'turn-1', deliver);
  expect(deliveries[0].result).toBe('the answer');
  expect(deliveries[0].error).toBeUndefined();
  expect(r.failed).toBeUndefined();
});
