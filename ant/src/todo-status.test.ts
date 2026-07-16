// Tests the TodoWrite → live status rendering + hook (spec 5/24). Pure funcs;
// the hook takes an injected sender so no MCP socket is needed.

import { test, expect } from 'bun:test';
import type { PostToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';
import { renderTodos, extractTodos, createTodoStatusHook, Todo } from './todo-status.js';

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

function todoInput(todos: Todo[]): PostToolUseHookInput {
  return { tool_name: 'TodoWrite', tool_input: { todos } } as unknown as PostToolUseHookInput;
}

test('hook sends the rendered checklist on TodoWrite', async () => {
  const sent: { turn_id: string; text: string }[] = [];
  const hook = createTodoStatusHook('turn-1', async p => { sent.push(p); });
  await hook(
    todoInput([{ content: 'a', status: 'completed' }, { content: 'b', status: 'pending' }]),
    'tu-1',
    {} as never,
  );
  expect(sent).toEqual([{ turn_id: 'turn-1', text: '☑ a\n☐ b' }]);
});

test('hook ignores non-TodoWrite tools and an empty turn id', async () => {
  const sent: unknown[] = [];
  const push = async (p: { turn_id: string; text: string }) => { sent.push(p); };
  await createTodoStatusHook('turn-1', push)(
    { tool_name: 'Bash', tool_input: {} } as unknown as PostToolUseHookInput, 't', {} as never,
  );
  await createTodoStatusHook('', push)(
    todoInput([{ content: 'a', status: 'pending' }]), 't', {} as never,
  );
  expect(sent).toHaveLength(0);
});

test('hook ignores a single-item list (not multi-step)', async () => {
  const sent: unknown[] = [];
  await createTodoStatusHook('turn-1', async p => { sent.push(p); })(
    todoInput([{ content: 'only one', status: 'in_progress' }]), 't', {} as never,
  );
  expect(sent).toHaveLength(0);
});
