import { test, expect } from 'bun:test';
import type { ToolEvent } from './policy.js';
import { todoStatus } from './todo-status.js';

/** A native event reaches routd unchanged. */
test('sends the raw snapshot with its source', async () => {
  const snapshot: ToolEvent = { hook_event_name: 'PostToolUse', tool_name: 'update_plan', tool_input: { plan: [{ step: 'build', status: 'pending' }] }, session_id: 's', tool_use_id: 'c' };
  const sent: unknown[] = [];
  await todoStatus('turn-1', 'codex', async p => { sent.push(p); })(snapshot);
  expect(sent).toEqual([{ turn_id: 'turn-1', source: 'codex', snapshot }]);
});
