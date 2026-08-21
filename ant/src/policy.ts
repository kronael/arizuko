// policy.ts — THE capability layer (spec 6/7 § "ant owns the capabilities,
// each harness renders the hooks").
//
// Before this file every capability was written as a claude SDK HookCallback
// and the event it bound to existed only as its POSITION inside claude.ts's
// `hooks: {...}` literal. Neither half was data, so codex and pi ran with no
// skill guard, no tool log and no steering — 5/23 degraded to an advisory scan
// there (BUGS L59).
//
// The split: a policy is a harness-neutral function (ToolEvent) => Verdict, and
// BINDINGS is the table saying where each one goes and what it may return.
// Three renderers read that one table — claude.ts wraps rows as SDK callbacks,
// codex.ts writes them to hooks.json, pi-extension.ts registers them in-process.
// The SCANNERS still live elsewhere (skillguard.ts owns the pattern table and
// the verdict, tool-log.ts the line format); this file owns the WIRING.

import { bashScrub, bashSkillRescan } from './guards.js';
import { skillGuard } from './skillguard.js';
import { toolLogPost, toolLogPre } from './tool-log.js';
import { todoStatus } from './todo-status.js';
import { drainSteer } from './steer.js';

// ToolEvent is the input shape claude and codex ALREADY agree on — measured
// 2026-08-21 by running codex 0.147.0: its hook stdin carries session_id, cwd,
// hook_event_name, tool_name, tool_input and tool_use_id under those exact
// keys. pi names the same facts differently, so pi-extension.ts maps them.
export interface ToolEvent {
  hook_event_name: PolicyEvent;
  tool_name: string;
  tool_input: Record<string, unknown>;
  tool_use_id: string;
  session_id: string;
  cwd?: string;
  tool_response?: unknown;
}

export type PolicyEvent = 'PreToolUse' | 'PostToolUse';

// Verdict is what a policy may answer. Three of the four are universal;
// 'rewrite' is claude-only (codex's PreToolUseDecisionWire is approve|block|
// allow, and updatedInput lives on its separate PermissionRequest event, so a
// rewrite returned there is silently ignored — measured, not read off a doc).
export type Verdict =
  | { kind: 'allow' }
  | { kind: 'deny'; reason: string }
  | { kind: 'inject'; context: string }
  | { kind: 'rewrite'; input: Record<string, unknown> };

export const ALLOW: Verdict = { kind: 'allow' };

// Binding is one row of the table. `returns` is the contract a renderer checks:
// a harness that cannot honour one of these refuses to render the row rather
// than emitting a gate that silently does nothing.
export interface Binding {
  id: string;
  event: PolicyEvent;
  matcher?: string; // regex over the tool name; absent = every tool
  returns: Verdict['kind'][];
  run(ev: ToolEvent): Promise<Verdict>;
}

// PolicyContext is the per-turn facts a policy needs that an event does not
// carry. It travels by argument, never by env var: a var ant reads that nothing
// emits as `-e` is silently dead, and that class already cost ARIZUKO_MODEL,
// ARIZUKO_BACKEND and ARIZUKO_IS_ROOT (BUGS L60).
export interface PolicyContext {
  home: string;
  turnID: string;
}

// bindings IS the table. Read it and you know every capability the agent runs
// under, on every harness.
export function bindings(ctx: PolicyContext): Binding[] {
  const rescan = bashSkillRescan(ctx.home);
  const todo = todoStatus(ctx.turnID);
  return [
    // A skill the agent writes today runs in its NEXT session, so this is the
    // gate between authoring and executing (spec 5/23). It scans the file the
    // write would PRODUCE, not the fragment it carries (BUGS J9).
    { id: 'skill-guard', event: 'PreToolUse', matcher: 'Write|Edit|MultiEdit', returns: ['deny'], run: async (ev) => skillGuard(ev) },
    // claude-only by nature: it unsets the model credential ahead of a shell
    // the agent drives, and only the claude harness puts that credential in the
    // environment. codex reads its own from $CODEX_HOME/auth.json.
    { id: 'bash-scrub', event: 'PreToolUse', matcher: 'Bash', returns: ['rewrite'], run: async (ev) => bashScrub(ev) },
    { id: 'tool-log-pre', event: 'PreToolUse', returns: [], run: async (ev) => toolLogPre(ev) },
    // The catch-all. The write gate above covers claude's write TOOLS, and
    // nothing else: Bash reaches the same files without them (BUGS J8), and
    // codex writes with `apply_patch` — measured 2026-08-21, so
    // `Write|Edit|MultiEdit` never fires there at all. Rather than enumerate
    // one tool name per harness and lose the next one silently, this row takes
    // NO matcher and rechecks the filesystem after every tool. The mtime gate
    // inside it keeps the cost to files that actually moved.
    { id: 'skill-rescan', event: 'PostToolUse', returns: [], run: async (ev) => rescan(ev) },
    { id: 'tool-log-post', event: 'PostToolUse', returns: [], run: async (ev) => toolLogPost(ev) },
    { id: 'steering', event: 'PostToolUse', returns: ['inject'], run: async () => drainSteer() },
    // TodoWrite is a claude tool name; codex plans with update_plan and pi has
    // no equivalent, so the matcher simply never fires there. Mapping those is
    // its own change.
    { id: 'todo-status', event: 'PostToolUse', matcher: 'TodoWrite', returns: [], run: (ev) => todo(ev) },
  ];
}

// matches applies a row's matcher against the CANONICAL tool name — the one
// claude and codex both use. pi lower-cases its built-ins, so its renderer
// canonicalizes at the boundary; a second, case-insensitive matching rule here
// would leave the policy BODIES still comparing 'Write' exactly.
export function matches(b: Binding, toolName: string): boolean {
  if (!b.matcher) return true;
  return new RegExp(`^(${b.matcher})$`).test(toolName);
}

// findBinding resolves a row by id — the codex hook subprocess's entry point.
export function findBinding(ctx: PolicyContext, id: string): Binding | undefined {
  return bindings(ctx).find((b) => b.id === id);
}
