// pi-extension.ts — the pi renderer (spec 6/7).
//
// pi's hooks are in-process TypeScript and pi loads an extension by explicit
// path (`-e`), which keeps working under `--no-extensions`. So this file
// imports the same binding table claude and codex render, and registers the two
// events it maps to. Nothing is generated; the only translation is pi's field
// names.

import os from 'os';
import path from 'path';
import { PolicyContext, ToolEvent, Verdict, bindings, runRows, runnableBy } from './policy.js';

// PI_HONOURS: pi can refuse a call ({block, reason}) and can rewrite a tool
// RESULT, which is how an injection reaches the model. It has no rewrite of a
// tool's INPUT, so that row is refused rather than silently skipped.
const PI_HONOURS: Array<Verdict['kind']> = ['deny', 'inject'];

// The two pi events the table binds to, in pi's own shape (docs/extensions.md,
// pi 0.51.6).
interface PiToolCallEvent {
  toolName: string;
  toolCallId: string;
  input: Record<string, unknown>;
}

interface PiToolResultEvent extends PiToolCallEvent {
  content?: unknown;
  isError?: boolean;
}

interface PiApi {
  on(event: 'tool_call', fn: (ev: PiToolCallEvent) => Promise<{ block: boolean; reason: string } | undefined>): void;
  on(event: 'tool_result', fn: (ev: PiToolResultEvent) => Promise<{ content: unknown[] } | undefined>): void;
}

// canonicalToolName maps pi's names onto the ones claude and codex share. pi
// calls them bash/write/edit where the others say Bash/Write/Edit — a pure case
// difference, so upper-casing the first letter is the whole mapping. It happens
// HERE, once, because the policy bodies compare canonical names too: skillguard
// reads `toolName === 'Write'` to find the fragment a write carries, so a
// lower-case name reached the guard with nothing to scan and passed.
export function canonicalToolName(name: string): string {
  return name ? name[0].toUpperCase() + name.slice(1) : name;
}

// canonicalInput maps pi's input keys onto the canonical (claude/codex) ones
// the policy bodies read. pi's write is {path, content} and its edit is
// {path, oldText, newText} (tools/write.d.ts, edit.d.ts — pi 0.51.6), so a
// guard reading `file_path` got '' and ALLOWED every pi write — a gate that
// silently did nothing, on the tool the skill-guard exists for. pi also
// accepts paths relative to the session cwd; the guard's path check needs
// them absolute, and process.cwd() here IS that cwd.
export function canonicalInput(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  const abs = (p: unknown) => (typeof p === 'string' && p !== '' ? path.resolve(p) : '');
  if (toolName === 'Write' || toolName === 'Read') {
    const { path: p, ...rest } = input;
    return { ...rest, file_path: abs(p) };
  }
  if (toolName === 'Edit') {
    const { path: p, oldText, newText, ...rest } = input;
    return { ...rest, file_path: abs(p), old_string: oldText, new_string: newText };
  }
  return input;
}

function toToolEvent(ev: PiToolCallEvent, event: ToolEvent['hook_event_name'], response?: unknown): ToolEvent {
  const toolName = canonicalToolName(ev.toolName);
  return {
    hook_event_name: event,
    tool_name: toolName,
    tool_input: canonicalInput(toolName, ev.input ?? {}),
    tool_use_id: ev.toolCallId,
    // pi carries no session id on a tool event. The tool log keys on it only to
    // group lines, so an empty value costs grouping, never a refusal.
    session_id: '',
    tool_response: response,
  };
}

// registerWith takes the context explicitly. The ambient os.homedir() read
// happens in the default export and nowhere else: skill-rescan RENAMES files
// under <home>/.claude/skills, so a caller that cannot choose the home cannot
// be tested without quarantining the developer's own skills.
export function registerWith(pi: PiApi, ctx: PolicyContext): void {
  const rows = runnableBy(bindings(ctx), PI_HONOURS, (m) => console.error(`[ant:pi] ${m}`));

  pi.on('tool_call', async (ev) => {
    const verdict = await runRows(rows, 'PreToolUse', toToolEvent(ev, 'PreToolUse'));
    return verdict.kind === 'deny' ? { block: true, reason: verdict.reason } : undefined;
  });

  pi.on('tool_result', async (ev) => {
    const verdict = await runRows(rows, 'PostToolUse', toToolEvent(ev, 'PostToolUse', ev.content));
    if (verdict.kind !== 'inject') return undefined;
    // pi's only channel back into the turn is the tool result itself, so the
    // injected context rides along with the output the model already reads.
    // ev.content is pi's (TextContent|ImageContent)[] (types.d.ts, pi 0.51.6)
    // and is kept verbatim — stringifying it printed "[object Object]" in
    // place of the real tool output on every steering injection.
    const existing = Array.isArray(ev.content) ? ev.content : [];
    return { content: [...existing, { type: 'text', text: verdict.context }] };
  });
}

// turnID is empty: pi has no live-checklist path (that row matches TodoWrite, a
// tool pi does not have), so nothing here needs the arizuko turn id.
export default function (pi: PiApi): void {
  registerWith(pi, { home: os.homedir(), turnID: '' });
}
