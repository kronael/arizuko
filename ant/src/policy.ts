// policy.ts — THE capability layer (spec 6/7 § "ant owns the capabilities,
// each harness renders the hooks").
//
// Before this file every capability was written as a claude SDK HookCallback,
// and the event it bound to existed only as its POSITION inside claude.ts's
// `hooks: {...}` literal. Neither half was data, so codex and pi ran with no
// skill guard, no tool log and no steering — 5/23 degraded to an advisory scan
// there (BUGS L59).
//
// Two things live here and nothing else:
//   - a POLICY is a function (ToolEvent) => Verdict, with no harness in it;
//   - BINDINGS is the table saying where each one goes and what it may answer.
// Three renderers read that table — claude.ts wraps rows as SDK callbacks,
// codex.ts writes them to hooks.json, pi-extension.ts registers them in-process.
// The SCANNERS still live elsewhere (skillguard.ts owns the pattern table and
// the verdict, tool-log.ts the line format); this file owns the WIRING.

import { bashScrub, bashSkillRescan } from './guards.js';
import { skillGuard } from './skillguard.js';
import { toolLogPost, toolLogPre } from './tool-log.js';
import { todoStatus } from './todo-status.js';
import { STEER_FILE, drainLines } from './spool.js';

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
// a harness that cannot honour one of these refuses the row rather than
// emitting a gate that silently does nothing.
export interface Binding {
  id: string;
  event: PolicyEvent;
  matcher?: string; // regex over the canonical tool name; absent = every tool
  returns: Array<Verdict['kind']>;
  run(ev: ToolEvent): Promise<Verdict>;
}

// PolicyContext is the per-turn facts a policy needs that an event does not
// carry. It travels by argument, never by env var, and never by an ambient
// os.homedir() read: skill-rescan RENAMES files under <home>/.claude/skills, so
// a caller that cannot choose the home cannot be tested safely.
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
    // The catch-all. The write gate above covers claude's write TOOLS and
    // nothing else: Bash reaches the same files without them (BUGS J8), and
    // codex writes with `apply_patch` — measured 2026-08-21, so
    // `Write|Edit|MultiEdit` never fires there at all. Rather than enumerate one
    // tool name per harness and lose the next one silently, this row takes NO
    // matcher and rechecks the filesystem after every tool. The mtime gate
    // inside it keeps the cost to files that actually moved.
    { id: 'skill-rescan', event: 'PostToolUse', returns: [], run: async (ev) => rescan(ev) },
    { id: 'tool-log-post', event: 'PostToolUse', returns: [], run: async (ev) => toolLogPost(ev) },
    { id: 'steering', event: 'PostToolUse', returns: ['inject'], run: async () => steering() },
    // TodoWrite is a claude tool name; codex plans with update_plan and pi has
    // no equivalent, so the matcher simply never fires there. Mapping those is
    // its own change.
    { id: 'todo-status', event: 'PostToolUse', matcher: 'TodoWrite', returns: [], run: (ev) => todo(ev) },
  ];
}

// steering delivers whatever the chassis queued since the last tool call. The
// queue is a file (spool.ts) because a closure cannot cross into codex's hook
// subprocess.
function steering(): Verdict {
  const messages = drainLines(STEER_FILE);
  if (messages.length === 0) return ALLOW;
  return { kind: 'inject', context: `<user-steering>\n${messages.join('\n')}\n</user-steering>` };
}

// matches applies a row's matcher against the CANONICAL tool name — the one
// claude and codex both use. pi lower-cases its built-ins, so its renderer
// canonicalizes at the boundary; a second, case-insensitive matching rule here
// would leave the policy BODIES still comparing 'Write' exactly.
export function matches(b: Binding, toolName: string): boolean {
  if (!b.matcher) return true;
  return new RegExp(`^(${b.matcher})$`).test(toolName);
}

// runnableBy is the contract check, shared by every out-of-process renderer.
// A row whose verdicts a harness cannot honour is REFUSED loudly rather than
// rendered into a gate that quietly does nothing — the failure mode this whole
// layer exists to remove.
export function runnableBy(rows: Binding[], honours: Array<Verdict['kind']>, log: (m: string) => void): Binding[] {
  return rows.filter((b) => {
    const missing = b.returns.filter((k) => !honours.includes(k));
    if (missing.length === 0) return true;
    log(`policy ${b.id} not rendered: cannot honour ${missing.join(', ')}`);
    return false;
  });
}

// runRow is the ONE place a policy is executed, on every harness. A policy that
// throws ALLOWS: a guard that bricks the turn when it throws is worse than the
// threat it was added for, and pi would otherwise diverge — its docs make a
// throwing `tool_call` handler block the tool, so an exception there would fail
// closed while claude and codex fail open.
export async function runRow(b: Binding, ev: ToolEvent): Promise<Verdict> {
  try {
    return await b.run(ev);
  } catch (err) {
    console.error(`[ant] policy ${b.id} threw, allowing: ${err instanceof Error ? err.message : String(err)}`);
    return ALLOW;
  }
}

// runRows applies every row bound to `event` and merges the answers: a deny
// wins outright, otherwise the injected texts join, otherwise allow. That is
// the precedence claude's SDK already applies across several hooks, stated
// ONCE here so codex's subprocess and pi's extension cannot drift from it.
//
// A 'rewrite' cannot arrive: both callers filter those rows out first
// (runnableBy), because neither harness can honour one.
export async function runRows(rows: Binding[], event: PolicyEvent, ev: ToolEvent): Promise<Verdict> {
  const injected: string[] = [];
  for (const b of rows) {
    if (b.event !== event || !matches(b, ev.tool_name)) continue;
    const verdict = await runRow(b, ev);
    if (verdict.kind === 'deny') return verdict;
    if (verdict.kind === 'inject') injected.push(verdict.context);
  }
  return injected.length > 0 ? { kind: 'inject', context: injected.join('\n') } : ALLOW;
}
