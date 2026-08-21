// pi-extension.ts — the pi renderer (spec 6/7).
//
// pi's hooks are in-process TypeScript and pi loads an extension by explicit
// path (`-e`), which keeps working with `--no-extensions` — so this file
// imports the same binding table claude and codex render and registers each row
// natively. Nothing is generated and nothing is translated but pi's field
// names: it calls the tool `bash`/`write`/`edit` where claude and codex say
// `Bash`/`Write`/`Edit`, so the matcher is applied case-insensitively.
//
// pi honours deny and inject. It has no rewrite, so a row that may rewrite is
// refused here for the same reason codex refuses it — a gate that silently does
// nothing is worse than no gate.

import os from 'os';
import { Binding, PolicyContext, ToolEvent, bindings, matches } from './policy.js';

// PiToolCallEvent / PiToolResultEvent are the two pi events the table binds to,
// named in pi's own shape (docs/extensions.md, pi 0.51.6).
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

function toToolEvent(ev: PiToolCallEvent, event: Binding['event'], response?: unknown): ToolEvent {
  return {
    hook_event_name: event,
    tool_name: canonicalToolName(ev.toolName),
    tool_input: ev.input ?? {},
    tool_use_id: ev.toolCallId,
    // pi does not carry a session id on a tool event; the tool log keys on it
    // only to group lines, so an empty value costs grouping, never a refusal.
    session_id: '',
    tool_response: response,
  };
}

function usable(rows: Binding[]): Binding[] {
  return rows.filter((b) => {
    if (!b.returns.includes('rewrite')) return true;
    console.error(`[ant:pi] policy ${b.id} not registered: pi cannot honour a 'rewrite' verdict`);
    return false;
  });
}

// registerWith takes the context explicitly. The ambient os.homedir() read
// happens in the default export and nowhere else: skill-rescan RENAMES files
// under <home>/.claude/skills, so a caller that cannot choose the home cannot
// be tested without quarantining the developer's own skills.
export function registerWith(pi: PiApi, ctx: PolicyContext): void {
  const rows = usable(bindings(ctx));

  pi.on('tool_call', async (ev) => {
    for (const b of rows.filter((r) => r.event === 'PreToolUse')) {
      if (!matches(b, canonicalToolName(ev.toolName))) continue;
      const verdict = await b.run(toToolEvent(ev, 'PreToolUse'));
      if (verdict.kind === 'deny') return { block: true, reason: verdict.reason };
    }
    return undefined;
  });

  pi.on('tool_result', async (ev) => {
    const injected: string[] = [];
    for (const b of rows.filter((r) => r.event === 'PostToolUse')) {
      if (!matches(b, canonicalToolName(ev.toolName))) continue;
      const verdict = await b.run(toToolEvent(ev, 'PostToolUse', ev.content));
      if (verdict.kind === 'inject') injected.push(verdict.context);
    }
    if (injected.length === 0) return undefined;
    // pi's only channel back into the turn is the tool result itself, so the
    // injected context rides along with the output the model already reads.
    return { content: [{ type: 'text', text: String(ev.content ?? '') }, ...injected.map((t) => ({ type: 'text', text: t }))] };
  });
}

// turnID is empty: pi has no live-checklist path (that row matches TodoWrite, a
// tool pi does not have), so nothing here needs the arizuko turn id.
export default function (pi: PiApi): void {
  registerWith(pi, { home: os.homedir(), turnID: '' });
}
