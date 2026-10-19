// Tests the TodoWrite → live status rendering + hook (spec 5/24). Pure funcs;
// the hook takes an injected sender so no MCP socket is needed.

import { test, expect } from 'bun:test';
import type { ToolEvent } from './policy.js';
import { renderTodos, extractTodos, todoStatus, Todo } from './todo-status.js';

test('renderTodos marks completed / in-progress / pending', () => {
  const todos: Todo[] = [
    { content: 'build', status: 'completed', activeForm: 'Building' },
    { content: 'deploy', status: 'in_progress', activeForm: 'Deploying' },
    { content: 'verify', status: 'pending', activeForm: 'Verifying' },
  ];
  expect(renderTodos(todos)).toBe('☑ build\n⏳ Deploying\n☐ verify');
});

test('renderTodos falls back to content when activeForm missing', () => {
  expect(renderTodos([{ content: 'ship', status: 'in_progress' }])).toBe('⏳ ship');
});

test('extractTodos rejects non-lists and empty', () => {
  expect(extractTodos(null)).toBeNull();
  expect(extractTodos({})).toBeNull();
  expect(extractTodos({ todos: [] })).toBeNull();
  expect(extractTodos({ todos: [{ content: 'x', status: 'pending' }] })).toHaveLength(1);
});

function todoInput(todos: Todo[]): ToolEvent {
  return { hook_event_name: 'PostToolUse', tool_name: 'TodoWrite', tool_input: { todos } } as ToolEvent;
}

test('the policy sends the rendered checklist on TodoWrite', async () => {
  const sent: { turn_id: string; text: string }[] = [];
  await todoStatus('turn-1', async p => { sent.push(p); })(
    todoInput([{ content: 'a', status: 'completed' }, { content: 'b', status: 'pending' }]),
  );
  expect(sent).toEqual([{ turn_id: 'turn-1', text: '☑ a\n☐ b', kind: 'progress' }]);
});

test('the policy ignores non-TodoWrite tools and an empty turn id', async () => {
  const sent: unknown[] = [];
  const push = async (p: { turn_id: string; text: string }) => { sent.push(p); };
  await todoStatus('turn-1', push)({ hook_event_name: 'PostToolUse', tool_name: 'Bash', tool_input: {} } as ToolEvent);
  await todoStatus('', push)(todoInput([{ content: 'a', status: 'pending' }]));
  expect(sent).toHaveLength(0);
});

test('the policy ignores a single-item list (not multi-step)', async () => {
  const sent: unknown[] = [];
  await todoStatus('turn-1', async p => { sent.push(p); })(
    todoInput([{ content: 'only one', status: 'in_progress' }]),
  );
  expect(sent).toHaveLength(0);
});
