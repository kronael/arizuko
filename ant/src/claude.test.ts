// The Claude Code harness drives every production turn, and its event mapping
// was covered only indirectly (BUGS F6.3). The surrounding path runs inside the
// SDK's query(), which needs real credentials, so the coverage is a direct test
// of normalize() — the load-bearing part.

import { test, expect } from 'bun:test';
import { normalize } from './claude.js';

// One case per SDK message shape normalize recognises. The runtime switches on
// Event.type alone (ant/src/index.ts runQuery), so a wrong mapping here
// silently drops a whole class of message.
test('normalize maps system/init to system_init and carries the session id', () => {
  const ev = normalize({ type: 'system', subtype: 'init', session_id: 'abc-123' });
  expect(ev).not.toBeNull();
  expect(ev!.type).toBe('system_init');
  expect(ev!.sessionId).toBe('abc-123');
  // runQuery assigns newSessionId from this and nothing else; final must be
  // falsy or the turn would end on init.
  expect(ev!.final).toBeFalsy();
});

test('normalize maps assistant and user messages to assistant / tool_result', () => {
  const a = normalize({ type: 'assistant', uuid: 'u1', message: { content: [] } });
  expect(a!.type).toBe('assistant');
  // runQuery reads uuid off raw for resumeAt — raw must be the message verbatim.
  expect((a!.raw as { uuid?: string }).uuid).toBe('u1');

  const u = normalize({ type: 'user', message: { content: [] } });
  expect(u!.type).toBe('tool_result');
});

test('normalize maps rate_limit_event and drops unknown message types', () => {
  expect(normalize({ type: 'rate_limit_event' })!.type).toBe('rate_limit');
  expect(normalize({ type: 'stream_event', delta: 'partial' })).toBeNull();
  expect(normalize({ noType: true })).toBeNull();
});

test('normalize maps a success result to a final ok event with usage', () => {
  const ev = normalize({
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
  expect(ev).not.toBeNull();
  expect(ev!.type).toBe('result');
  expect(ev!.final).toBe(true);
  expect(ev!.status).toBe('success');
  expect(ev!.text).toBe('the answer');
  // costUSD → cost_cents is × 100 then ROUNDED (spec 5/34). 0.0456 → 4.56 → 5;
  // truncation would give 4, so this value discriminates the two. The cache
  // fields cross names — cacheReadInputTokens → cache_read,
  // cacheCreationInputTokens → cache_write — and 7 ≠ 3 catches a swap.
  expect(ev!.models?.['claude-opus-5']).toEqual({
    input: 100, output: 20, cache_read: 7, cache_write: 3, cost_cents: 5,
  });
});

test('normalize maps error_during_execution to a final error event', () => {
  // runQuery keys its session-reset retry off raw.subtype (isSessionError), so
  // the subtype must survive into raw, and the event must still be final.
  const ev = normalize({ type: 'result', subtype: 'error_during_execution' });
  expect(ev!.type).toBe('result');
  expect(ev!.final).toBe(true);
  expect(ev!.status).toBe('error');
  expect((ev!.raw as { subtype?: string }).subtype).toBe('error_during_execution');
});

// index.test.ts already covers claudeResultStatus itself. What is untested is
// that normalize WIRES it in: a logged-out harness returns subtype 'success'
// with the /login prompt as its result text, and an event that carried
// status:'success' would deliver that prompt to the user as the agent's answer
// — the shape of the fleet-wide outage after the v0.62 env regeneration.
test('normalize routes the not-logged-in result through claudeResultStatus', () => {
  const ev = normalize({ type: 'result', subtype: 'success', result: 'Not logged in · Please run /login' });
  expect(ev!.status).toBe('error');
  // Still delivered as text, so the operator sees WHY the turn failed.
  expect(ev!.text).toBe('Not logged in · Please run /login');
});
