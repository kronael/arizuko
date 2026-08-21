// The chassis <status> pipeline: statusBlocks is THE one extraction site —
// every harness's status frames flow through it, so a harness only has to put
// its assistant text on the frame (the pre-seam runtime read claude's raw
// message shape instead, and codex streamed no interim status at all).

import { test, expect } from 'bun:test';
import { NUDGE_TEXT, checkIpcMessage, runTurn, statusBlocks } from './index.js';
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
    profile: { continuity: 'none', tools: 'none', guard: 'none', steering: 'between-turn', status: 'streaming' },
    run: async function* () { yield* frames; },
  };
}

interface Delivered {
  status: string;
  error?: string;
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
// only when a tool call follows. A turn that stopped calling tools stranded
// those messages: the loop read only the IPC dir, saw nothing, and exited —
// the message was consumed and never delivered.
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
