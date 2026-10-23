// policy.ts — the capability layer (spec 6/16). A POLICY is (ToolEvent) =>
// Verdict with no harness in it; BINDINGS is the table saying where each one
// goes and what it may answer. Three renderers read that table: claude.ts wraps
// rows as SDK callbacks, codex.ts writes them to hooks.json, pi-extension.ts
// registers them in-process. The scanners live elsewhere (skillguard.ts owns
// the pattern table, tool-log.ts the line format); this file owns the wiring.

import { bashScrub, bashSkillRescan, ghWriteGate } from './guards.js';
import { skillGuard } from './skillguard.js';
import { toolLogPost, toolLogPre } from './tool-log.js';
import { todoStatus } from './todo-status.js';
import { replyOwed } from './reply-owed.js';
import { STEER_SPOOL, drainLines } from './spool.js';

// ToolEvent is the shape claude and codex already agree on — measured against
// codex 0.147.0: its hook stdin carries these exact keys. pi names the same
// facts differently, so pi-extension.ts maps them.
export interface ToolEvent {
  hook_event_name: PolicyEvent;
  tool_name: string;
  tool_input: Record<string, unknown>;
  tool_use_id: string;
  session_id: string;
  cwd?: string;
  tool_response?: unknown;
  stop_hook_active?: boolean;
  // claude's Stop input lists the tasks still running; codex and pi never send it.
  background_tasks?: unknown[];
  // claude fires its hooks for a subagent's tool calls too and names the subagent here.
  agent_id?: string;
}

export type PolicyEvent = 'PreToolUse' | 'PostToolUse' | 'Stop';

// Verdict is what a policy may answer. 'rewrite' is claude-only: codex's
// PreToolUse decision is approve|block|allow and updatedInput lives on its
// separate PermissionRequest event, so a rewrite there is silently ignored.
export type Verdict =
  | { kind: 'allow' }
  | { kind: 'deny'; reason: string }
  | { kind: 'inject'; context: string }
  | { kind: 'rewrite'; input: Record<string, unknown> };

export const ALLOW: Verdict = { kind: 'allow' };

// `returns` is the contract a renderer checks: a harness that cannot honour one
// of these refuses the row rather than emitting a gate that does nothing.
export interface Binding {
  id: string;
  event: PolicyEvent;
  matcher?: string; // regex over the canonical tool name; absent = every tool
  returns: Array<Verdict['kind']>;
  run(ev: ToolEvent): Promise<Verdict>;
}

// PolicyContext travels by argument, never by env var or an ambient
// os.homedir() read: skill-rescan RENAMES files under <home>/.claude/skills, so
// a caller that cannot choose the home cannot be tested safely.
export interface PolicyContext {
  home: string;
  turnID: string;
}

// bindings IS the table: every capability the agent runs under, on every harness.
export function bindings(ctx: PolicyContext): Binding[] {
  const rescan = bashSkillRescan(ctx.home);
  const todo = todoStatus(ctx.turnID);
  return [
    { id: 'reply-owed', event: 'Stop', returns: ['deny'], run: replyOwed(ctx.turnID) },
    // A skill the agent writes today runs in its NEXT session. Scans the file the
    // write would PRODUCE, not the fragment it carries (BUGS J9).
    { id: 'skill-guard', event: 'PreToolUse', matcher: 'Write|Edit|MultiEdit', returns: ['deny'], run: async (ev) => skillGuard(ev) },
    // claude-only because it returns 'rewrite', which codex and pi cannot honour
    // — NOT because they hold no credential. readSecrets ships every
    // store.EnvProfileKeys value into the container on every backend, so this row
    // being dropped leaves them readable from those agents' shells (BUGS L131).
    { id: 'bash-scrub', event: 'PreToolUse', matcher: 'Bash', returns: ['rewrite'], run: async (ev) => bashScrub(ev) },
    // Refuses the GitHub writes a folder must not make once it holds a shell
    // credential. 'deny' rather than 'rewrite', so unlike bash-scrub above this
    // row runs on codex and pi too.
    { id: 'gh-write-gate', event: 'PreToolUse', matcher: 'Bash', returns: ['deny'], run: async (ev) => ghWriteGate(ev) },
    { id: 'tool-log-pre', event: 'PreToolUse', returns: [], run: async (ev) => toolLogPre(ev) },
    // The catch-all. The write gate above covers claude's write TOOLS only: Bash
    // reaches the same files without them (BUGS J8), and codex writes with
    // `apply_patch`, so `Write|Edit|MultiEdit` never fires there. No matcher —
    // it rechecks the filesystem after every tool, mtime-gated.
    { id: 'skill-rescan', event: 'PostToolUse', returns: [], run: async (ev) => rescan(ev) },
    { id: 'tool-log-post', event: 'PostToolUse', returns: [], run: async (ev) => toolLogPost(ev) },
    { id: 'steering', event: 'PostToolUse', returns: ['inject'], run: async (ev) => steering(ev) },
    // TodoWrite is a claude tool name; codex plans with update_plan and pi has no
    // equivalent, so the matcher never fires there.
    { id: 'todo-status', event: 'PostToolUse', matcher: 'TodoWrite', returns: [], run: (ev) => todo(ev) },
  ];
}

// The queue is a file (spool.ts) because a closure cannot cross into codex's
// hook subprocess.
function steering(ev: ToolEvent): Verdict {
  if (ev.agent_id) return ALLOW;
  const messages = drainLines(STEER_SPOOL);
  if (messages.length === 0) return ALLOW;
  return { kind: 'inject', context: `<user-steering>\n${messages.join('\n')}\n</user-steering>` };
}

// matches applies a row's matcher against the CANONICAL tool name. pi
// lower-cases its built-ins, so its renderer canonicalizes at the boundary.
export function matches(b: Binding, toolName: string): boolean {
  if (!b.matcher) return true;
  return new RegExp(`^(${b.matcher})$`).test(toolName);
}

// runnableBy is the contract check. A row whose verdicts a harness cannot
// honour is REFUSED loudly rather than rendered into a gate that does nothing.
export function runnableBy(rows: Binding[], honours: Array<Verdict['kind']>, log: (m: string) => void): Binding[] {
  return rows.filter((b) => {
    const missing = b.returns.filter((k) => !honours.includes(k));
    if (missing.length === 0) return true;
    log(`policy ${b.id} not rendered: cannot honour ${missing.join(', ')}`);
    return false;
  });
}

// A policy that throws ALLOWS. pi would otherwise diverge — its docs make a
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

// runRows merges the answers: a deny wins outright, otherwise the injected
// texts join, otherwise allow — claude's SDK precedence, stated once here so
// codex's subprocess and pi's extension cannot drift from it.
//
// Every matching row RUNS even when one denies: an early return would record a
// refused call on claude and miss it on codex and pi, losing the most
// interesting tool-log lines. A 'rewrite' cannot arrive — both callers filter
// those rows out first.
export async function runRows(rows: Binding[], event: PolicyEvent, ev: ToolEvent): Promise<Verdict> {
  let deny: Verdict | undefined;
  const injected: string[] = [];
  for (const b of rows) {
    if (b.event !== event || !matches(b, ev.tool_name)) continue;
    const verdict = await runRow(b, ev);
    if (verdict.kind === 'deny' && !deny) deny = verdict;
    if (verdict.kind === 'inject') injected.push(verdict.context);
  }
  if (deny) return deny;
  return injected.length > 0 ? { kind: 'inject', context: injected.join('\n') } : ALLOW;
}
