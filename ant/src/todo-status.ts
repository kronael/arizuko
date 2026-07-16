// Live tasklist status: a PostToolUse hook on the SDK's TodoWrite tool renders
// the whole task list into ONE "⏳ …" status message per turn (spec 5/24).
// Progress reporting is deterministic bookkeeping over the agent's own todo
// list — the harness emits it, so it never depends on the model remembering to
// narrate progress (the old `progress` skill). routd edits the same message in
// place on each update, so the user sees a live checklist, not a stream of pings.
//
// Hooks must never throw — all errors are swallowed.

import { HookCallback, PostToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';
import { submitStatus } from './mcp.js';

export interface Todo {
  content: string;
  status: 'pending' | 'in_progress' | 'completed';
  activeForm?: string;
}

const MARK: Record<Todo['status'], string> = {
  completed: '☑',
  in_progress: '⏳',
  pending: '☐',
};

// renderTodos turns a TodoWrite list into a checklist string. An in-progress
// item shows its activeForm ("Deploying…") when present, else its content.
export function renderTodos(todos: Todo[]): string {
  return todos
    .map(t => {
      const label = t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content;
      return `${MARK[t.status] ?? '☐'} ${label}`;
    })
    .join('\n');
}

// extractTodos pulls a well-formed todo array out of the TodoWrite tool input,
// or null when the shape is missing/empty.
export function extractTodos(toolInput: unknown): Todo[] | null {
  if (!toolInput || typeof toolInput !== 'object') return null;
  const todos = (toolInput as { todos?: unknown }).todos;
  if (!Array.isArray(todos)) return null;
  const out = todos.filter(
    (t): t is Todo => !!t && typeof t === 'object' && typeof (t as Todo).content === 'string',
  );
  return out.length > 0 ? out : null;
}

// createTodoStatusHook returns a PostToolUse hook that, on each TodoWrite,
// forwards the rendered checklist to routd as a mid-turn status. turnID is the
// arizuko turn id (empty → no-op). send is injectable for tests; defaults to the
// real submit_status RPC.
export function createTodoStatusHook(
  turnID: string,
  send: (p: { turn_id: string; text: string }) => Promise<void> = submitStatus,
): HookCallback {
  return async (input, _toolUseId, _context) => {
    try {
      const pi = input as PostToolUseHookInput;
      if (pi.tool_name !== 'TodoWrite' || !turnID) return {};
      const todos = extractTodos(pi.tool_input);
      if (!todos) return {};
      await send({ turn_id: turnID, text: renderTodos(todos) });
    } catch {
      // never let a status update break a tool call
    }
    return {};
  };
}
