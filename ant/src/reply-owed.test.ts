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

/** A Stop while background tasks run is a pause, not the end: allowed, no check. An empty list or an absent field (codex) checks. */
test('reply owed allows a Stop while background tasks run', async () => {
  let calls = 0;
  const check = async () => { calls++; return { nudge: 'deliver' }; };
  const policy = replyOwed('t1', check);
  const tasks = [{ id: 'a1', type: 'subagent', status: 'running', description: 'review' }];
  expect(await policy({ ...event, background_tasks: tasks })).toEqual(ALLOW);
  expect(calls).toBe(0);
  expect(await policy({ ...event, background_tasks: [] })).toEqual({ kind: 'deny', reason: 'deliver' });
  expect(await policy(event)).toEqual({ kind: 'deny', reason: 'deliver' });
  expect(calls).toBe(2);
});

/** The Stop hook input's background_tasks reaches the policy through claudeHooks: tasks running -> no block, none -> block. */
test('claudeHooks hands background_tasks from the Stop input to reply-owed', async () => {
  const row = { id: 'reply-owed', event: 'Stop' as const, returns: ['deny' as const], run: replyOwed('t1', async () => ({ nudge: 'deliver' })) };
  const hook = claudeHooks([row]).Stop[0].hooks[0];
  const stop = { hook_event_name: 'Stop', session_id: 's', cwd: '/tmp', transcript_path: '/tmp/fixture', stop_hook_active: false };
  const ctx = { signal: new AbortController().signal };
  expect(await hook({ ...stop, background_tasks: [{ id: 'a1' }] } as never, undefined, ctx)).toEqual({});
  expect(await hook({ ...stop, background_tasks: [] } as never, undefined, ctx)).toEqual({ decision: 'block', reason: 'deliver' });
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
