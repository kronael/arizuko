import { test, expect } from 'bun:test';
import { bindings, runRows, type ToolEvent } from './policy.js';
import { todoStatus } from './todo-status.js';
import { STEER_SPOOL, NUDGE_TEXT, drainLines } from './spool.js';

/** A native event reaches routd unchanged. */
test('sends the raw snapshot with its source', async () => {
  const snapshot: ToolEvent = { hook_event_name: 'PostToolUse', tool_name: 'update_plan', tool_input: { plan: [{ step: 'build', status: 'pending' }] }, session_id: 's', tool_use_id: 'c' };
  const sent: unknown[] = [];
  await todoStatus('turn-1', 'codex', async p => { sent.push(p); return { remind: false }; })(snapshot);
  expect(sent).toEqual([{ turn_id: 'turn-1', source: 'codex', snapshot }]);
});

/** A reminder decision enters the same steering spool used by waiting Claude sessions. */
test('queues the reminder decision', async () => {
  drainLines(STEER_SPOOL);
  const snapshot: ToolEvent = { hook_event_name: 'PostToolUse', tool_name: 'Read', tool_input: {}, session_id: 's', tool_use_id: 'c' };
  await todoStatus('turn-1', 'claude', async () => ({ remind: true }))(snapshot);
  expect(drainLines(STEER_SPOOL)).toEqual([NUDGE_TEXT]);
});

/** The triggering completion receives the queued reminder in the same hook. */
test('the completion carries its reminder to steering', async () => {
  drainLines(STEER_SPOOL);
  const snapshot: ToolEvent = { hook_event_name: 'PostToolUse', tool_name: 'Read', tool_input: {}, session_id: 's', tool_use_id: 'c' };
  const rows = bindings({ home: '/tmp', turnID: 'turn-1' }, async () => ({ remind: true })).filter(row => row.id === 'completion');
  const verdict = await runRows(rows, 'PostToolUse', snapshot);
  expect(verdict.kind === 'inject' && verdict.context).toContain(NUDGE_TEXT);
  expect(drainLines(STEER_SPOOL)).toEqual([]);
});
