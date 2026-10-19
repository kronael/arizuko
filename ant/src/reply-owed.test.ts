import { expect, test } from 'bun:test';
import { replyOwed } from './reply-owed.js';
import { ALLOW, bindings, runRow, type ToolEvent } from './policy.js';
import { claudeHooks } from './claude.js';
import { verdictToCodexJson } from './codex.js';

const event: ToolEvent = {
  hook_event_name: 'Stop', tool_name: '', tool_input: {}, tool_use_id: '', session_id: 'session',
};

/** A Stop continuation skips the socket, as does a missing turn binding. */
test('reply owed skips recursive Stop and unbound turns', async () => {
  let calls = 0;
  const check = async () => { calls++; return { nudge: 'deliver' }; };
  expect(await replyOwed('', check)(event)).toEqual(ALLOW);
  expect(await replyOwed('t1', check)({ ...event, stop_hook_active: true })).toEqual(ALLOW);
  expect(calls).toBe(0);
});

/** Only routd's nonempty nudge blocks Stop; a socket failure follows the policy fail-open rule. */
test('reply owed uses the shared decision and fails open', async () => {
  const seen: string[] = [];
  const policy = replyOwed('t1', async (id) => { seen.push(id); return { nudge: 'deliver' }; });
  expect(await policy(event)).toEqual({ kind: 'deny', reason: 'deliver' });
  expect(seen).toEqual(['t1']);
  expect(await replyOwed('t1', async () => ({ nudge: '' }))(event)).toEqual(ALLOW);
  const row = { id: 'reply-owed', event: 'Stop' as const, returns: ['deny' as const], run: replyOwed('t1', async () => { throw new Error('closed'); }) };
  expect(await runRow(row, event)).toEqual(ALLOW);
  expect(bindings({ home: '/tmp', turnID: 't1' }).find(row => row.id === 'reply-owed')?.event).toBe('Stop');
});

/** Both harnesses consume the measured Stop block shape, with the reason intact. */
test('Stop renders a block for Claude and Codex', async () => {
  const row = { id: 'reply-owed', event: 'Stop' as const, returns: ['deny' as const], run: replyOwed('t1', async () => ({ nudge: 'deliver' })) };
  const hook = claudeHooks([row]).Stop[0].hooks[0];
  expect(await hook({ hook_event_name: 'Stop', session_id: 's', cwd: '/tmp', transcript_path: '/tmp/fixture', stop_hook_active: false }, undefined, { signal: new AbortController().signal }))
    .toEqual({ decision: 'block', reason: 'deliver' });
  expect(JSON.parse(verdictToCodexJson({ kind: 'deny', reason: 'deliver' }, 'Stop')))
    .toEqual({ decision: 'block', reason: 'deliver' });
});
