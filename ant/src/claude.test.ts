// The Claude Code harness drives every production turn and its message mapping
// was covered only indirectly (BUGS F6.3). The surrounding path runs inside the
// SDK's query(), which needs real credentials, so this tests ClaudeTurn directly.

import { test, expect } from 'bun:test';
import path from 'path';
import {
  ClaudeTurn,
  claudeEnv,
  claudeResultStatus,
  isRateLimitRejected,
  packContinuity,
  permanentAPIError,
  resolveResumeSession,
  transcriptPath,
  unpackContinuity,
} from './claude.js';

const UUID = 'b6c2b287-0000-4000-8000-000000000000';
const MSG_UUID = 'a1a1a1a1-0000-4000-8000-000000000000';
const never = () => false;
const always = () => true;

// --- continuity token ---------------------------------------------------

test('packContinuity/unpackContinuity round-trip the pair', () => {
  const token = packContinuity(UUID, MSG_UUID);
  expect(token).toBe(`${UUID}#${MSG_UUID}`);
  expect(unpackContinuity(token)).toEqual({ sessionId: UUID, resumeAt: MSG_UUID });
});

test('unpackContinuity: a bare platform id is a valid token (cross-spawn shape)', () => {
  // routd hands back the plain sessionId a continuity frame carried; the
  // harness must accept it as a token with no resume point.
  expect(unpackContinuity(UUID)).toEqual({ sessionId: UUID });
  expect(unpackContinuity(undefined)).toEqual({});
});

// --- resume guard (stale chat-bound session) -----------------------------

test('resolveResumeSession: UUID with a transcript on disk resumes it', () => {
  expect(resolveResumeSession(UUID, always)).toEqual({ resume: UUID });
});

test('resolveResumeSession: UUID with NO transcript is created under that id, not resumed', () => {
  // The pruned/never-persisted case — must not be passed to --resume, which
  // would return "No conversation found" → error_during_execution. The id is
  // the one routd persisted before the run, so a turn killed before submit_turn
  // leaves its transcript where the next turn resumes it.
  expect(resolveResumeSession(UUID, never)).toEqual({ sessionId: UUID });
});

test('resolveResumeSession: non-UUID placeholder starts fresh even if a file exists', () => {
  expect(resolveResumeSession('sess-123abc', always)).toEqual({});
});

test('resolveResumeSession: a non-UUID short-circuits before the disk check', () => {
  // The UUID shape is the cheap guard; a lineage placeholder must never cost a
  // stat call, and must never be resumed regardless of what is on disk.
  let calls = 0;
  const counting = () => { calls++; return true; };
  expect(resolveResumeSession('sess-placeholder', counting)).toEqual({});
  expect(calls).toBe(0);
});

test('transcriptPath uses the fixed -home-node slug', () => {
  expect(transcriptPath(UUID)).toBe(
    path.join('/home/node', '.claude', 'projects', '-home-node', `${UUID}.jsonl`),
  );
});

// --- ClaudeTurn: one frame class per SDK message shape -------------------

test('ClaudeTurn: system/init yields a continuity frame with the bare id', () => {
  const t = new ClaudeTurn();
  const frames = t.push({ type: 'system', subtype: 'init', session_id: UUID });
  expect(frames).toEqual([{ kind: 'continuity', token: UUID, sessionId: UUID }]);
});

test('ClaudeTurn: an assistant message packs the resume point and carries its text', () => {
  const t = new ClaudeTurn();
  t.push({ type: 'system', subtype: 'init', session_id: UUID });
  const frames = t.push({
    type: 'assistant',
    uuid: MSG_UUID,
    message: { content: [{ type: 'text', text: '<status>working…</status>' }] },
  });
  expect(frames).toEqual([
    { kind: 'continuity', token: `${UUID}#${MSG_UUID}`, sessionId: UUID },
    { kind: 'status', text: '<status>working…</status>' },
  ]);
});

test('ClaudeTurn: tool_result and unknown message types yield nothing', () => {
  const t = new ClaudeTurn();
  expect(t.push({ type: 'user', message: { content: [] } })).toEqual([]);
  expect(t.push({ type: 'stream_event', delta: 'partial' })).toEqual([]);
  expect(t.push({ noType: true })).toEqual([]);
});

test('ClaudeTurn: a success result is an ok frame with usage', () => {
  const t = new ClaudeTurn();
  const frames = t.push({
    type: 'result',
    subtype: 'success',
    result: 'the answer',
    modelUsage: {
      'claude-opus-5': {
        inputTokens: 100, outputTokens: 20,
        cacheReadInputTokens: 7, cacheCreationInputTokens: 3,
        costUSD: 0.0456,
      },
    },
  });
  expect(frames).toHaveLength(1);
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(true);
  expect(r.text).toBe('the answer');
  expect(r.retryFresh).toBeUndefined();
  // costUSD → cost_cents is × 100 then ROUNDED (spec 6/39). 0.0456 → 4.56 → 5;
  // truncation would give 4. The cache fields cross names, and 7 ≠ 3 catches a swap.
  expect(r.usage?.['claude-opus-5']).toEqual({
    input: 100, output: 20, cache_read: 7, cache_write: 3, cost_cents: 5,
  });
});

test('ClaudeTurn: error_during_execution is a retryFresh error frame — never a reply', () => {
  // The resume-miss ("No conversation found") case: the chassis retries the
  // turn with no continuity instead of delivering the harness diagnostic
  // (BUGS: stale chat-bound session).
  const frames = new ClaudeTurn().push({ type: 'result', subtype: 'error_during_execution' });
  expect(frames).toHaveLength(1);
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(false);
  expect(r.retryFresh).toBe(true);
});

test('claudeResultStatus rejects the SDK login sentinel', () => {
  expect(claudeResultStatus('success', 'Not logged in · Please run /login')).toBe('error');
});

test('claudeResultStatus preserves normal result status', () => {
  expect(claudeResultStatus('success', 'completed')).toBe('success');
  expect(claudeResultStatus('error_max_turns', 'stopped')).toBe('error');
});

// The logged-out harness returns subtype 'success' with the /login prompt as its
// result text; ok:true would deliver that prompt to the user as the answer —
// the fleet-wide outage after the v0.62 env regeneration.
test('ClaudeTurn routes the not-logged-in result through claudeResultStatus', () => {
  const frames = new ClaudeTurn().push({
    type: 'result', subtype: 'success', result: 'Not logged in · Please run /login',
  });
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(false);
  // Still delivered as text, so the operator sees WHY the turn failed.
  expect(r.text).toBe('Not logged in · Please run /login');
});

// --- rate limit ----------------------------------------------------------

/** Rejected usage events identify a refusal. */
test('isRateLimitRejected: rejected status identifies a refusal', () => {
  expect(isRateLimitRejected({ rate_limit_info: { status: 'rejected' } })).toBe(true);
});

test('isRateLimitRejected: allowed/allowed_warning and missing info stay silent', () => {
  expect(isRateLimitRejected({ rate_limit_info: { status: 'allowed' } })).toBe(false);
  expect(isRateLimitRejected({ rate_limit_info: { status: 'allowed_warning' } })).toBe(false);
  expect(isRateLimitRejected({})).toBe(false);
});

/** Usage events, including repeated refusals, emit no interim notice. */
test('ClaudeTurn: rate limit events stay silent', () => {
  const t = new ClaudeTurn();
  for (const status of ['allowed', 'allowed_warning', 'rejected', 'rejected']) {
    expect(t.push({ type: 'rate_limit_event', rate_limit_info: { status } })).toEqual([]);
  }
});

// --- model gateway --------------------------------------------------------

// No gateway: the SDK receives the very same object, so today's env is untouched.
test('claudeEnv: no gateway returns the same env object', () => {
  const env = { ANTHROPIC_API_KEY: 'real', HOME: '/home/node' };
  expect(claudeEnv(env)).toBe(env);
});

// Measured 2026-09-04: the SDK subprocess reads ANTHROPIC_BASE_URL and sends
// ANTHROPIC_AUTH_TOKEN as `Authorization: Bearer <token>`. The rest of env
// survives — HOME, the socket paths, everything the turn already carried.
test('claudeEnv: a gateway becomes ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN', () => {
  const out = claudeEnv({ HOME: '/home/node', MODEL_GATEWAY_URL: 'http://gw:4000', MODEL_GATEWAY_KEY: 'virtual-key-abc123' });
  expect(out.ANTHROPIC_BASE_URL).toBe('http://gw:4000');
  expect(out.ANTHROPIC_AUTH_TOKEN).toBe('virtual-key-abc123');
  expect(out.HOME).toBe('/home/node');
});

// URL without a key: the harness's own credential still authenticates, so no
// empty token is written over it.
test('claudeEnv: URL without key sets only the base URL', () => {
  const out = claudeEnv({ MODEL_GATEWAY_URL: 'http://gw:4000' });
  expect(out.ANTHROPIC_BASE_URL).toBe('http://gw:4000');
  expect('ANTHROPIC_AUTH_TOKEN' in out).toBe(false);
});

// A model API failure arrives as subtype 'success' with is_error true and the
// error line as the result text. Reading the subtype alone delivered
// "API Error: 400 …" to the user as the agent's answer, wrote no cost row, and
// left the run looking clean to the circuit breaker (BUGS L175).
test('claudeResultStatus honours the SDK is_error verdict over the subtype', () => {
  expect(claudeResultStatus('success', 'API Error: 400 fake says no', true)).toBe('error');
  expect(claudeResultStatus('success', 'a real answer', false)).toBe('success');
});

test('ClaudeTurn marks an is_error result as a failure', () => {
  // A 503 is transient, so this one stays retryable — which is what makes the
  // 400 case below a classification and not a blanket change.
  const frames = new ClaudeTurn().push({
    type: 'result', subtype: 'success', is_error: true, result: 'API Error: 503 upstream',
  });
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(false);
  expect(r.terminal).toBeUndefined();
});

// Re-sending a request the API already refused spawns another container to
// receive the same refusal. routd retried a 400 three times before telling
// anyone (BUGS L349); terminal makes it one spawn and names the cause.
test('a request the API refuses is terminal, a transient one is not', () => {
  for (const code of [400, 401, 403, 404, 405, 410, 422]) {
    expect(permanentAPIError(`API Error: ${code} nope`)).toBe(true);
  }
  for (const code of [408, 425, 429, 500, 502, 503]) {
    expect(permanentAPIError(`API Error: ${code} later`)).toBe(false);
  }
  expect(permanentAPIError('the harness failed this turn')).toBe(false);
  expect(permanentAPIError(undefined)).toBe(false);
});

test('ClaudeTurn marks a refused request terminal so routd does not respawn', () => {
  const frames = new ClaudeTurn().push({
    type: 'result', subtype: 'success', is_error: true, result: 'API Error: 400 fake says no',
  });
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(false);
  expect(r.terminal).toBe(true);
});

// sloth's seven-day cap reset eleven hours out, so re-spawning the turn only
// burns runs. A refusal seen during the turn makes its failure terminal.
test('ClaudeTurn marks a failure after a rejected rate limit as terminal', () => {
  const turn = new ClaudeTurn();
  turn.push({ type: 'rate_limit_event', rate_limit_info: { status: 'rejected' } });
  const frames = turn.push({
    type: 'result', subtype: 'success', is_error: true,
    result: "You've hit your individual spend limit",
  });
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.ok).toBe(false);
  expect(r.terminal).toBe(true);
  expect(r.text).toBe("You've hit your individual spend limit");
});

// An informational rate-limit event is not a refusal, so it must not make the
// next failure terminal — that would stop retrying a merely flaky turn.
test('ClaudeTurn leaves a failure retryable when the rate limit was informational', () => {
  const turn = new ClaudeTurn();
  turn.push({ type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } });
  const frames = turn.push({ type: 'result', subtype: 'success', is_error: true, result: 'API Error: 529' });
  const r = frames[0];
  if (r.kind !== 'result') throw new Error('expected result frame');
  expect(r.terminal).toBeUndefined();
});
