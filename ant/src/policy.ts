// policy.ts — the capability layer (spec 6/16). A POLICY is (ToolEvent) =>
// Verdict with no harness in it; BINDINGS is the table saying where each one
// goes. Three renderers read that table: claude.ts wraps
// rows as SDK callbacks, codex.ts writes them to hooks.json, pi-extension.ts
// registers them in-process. The scanners live elsewhere (skillguard.ts owns
// the pattern table, tool-log.ts the line format); this file owns the wiring.

import { bashSkillRescan, ghWriteGate } from './guards.js';
import { skillGuard } from './skillguard.js';
import { toolLogPost, toolLogPre } from './tool-log.js';
import { submitStatus } from './mcp.js';
import { replyOwed } from './reply-owed.js';
import { STEER_SPOOL, drainLines, nudgeProgress } from './spool.js';
import { spawn } from 'child_process';
import path from 'path';

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

export type PolicyEvent = 'PreToolUse' | 'PostToolUse' | 'PostToolUseFailure' | 'Stop';

// Verdict is what a policy may answer.
export type Verdict =
  | { kind: 'allow' }
  | { kind: 'deny'; reason: string }
  | { kind: 'inject'; context: string };

export const ALLOW: Verdict = { kind: 'allow' };

export interface Binding {
  id: string;
  event: PolicyEvent;
  matcher?: string; // regex over the canonical tool name; absent = every tool
  run(ev: ToolEvent): Promise<Verdict>;
}

// PolicyContext travels by argument, never by env var or an ambient
// os.homedir() read: skill-rescan RENAMES files under <home>/.claude/skills, so
// a caller that cannot choose the home cannot be tested safely.
export interface PolicyContext {
  home: string;
  turnID: string;
  source?: 'claude' | 'codex';
}

// bindings IS the table: every capability the agent runs under, on every harness.
export function bindings(ctx: PolicyContext, send: typeof submitStatus = submitStatus): Binding[] {
  const rescan = bashSkillRescan(ctx.home);
  const completion = async (ev: ToolEvent) => {
    try {
      if (ctx.turnID && (await send({ turn_id: ctx.turnID, source: ctx.source ?? 'claude', snapshot: ev })).remind) nudgeProgress();
    } catch (err) {
      console.error(`[ant] policy todo-status threw, allowing: ${err instanceof Error ? err.message : String(err)}`);
    }
    return steering(ev);
  };
  return [
    { id: 'reply-owed', event: 'Stop', run: replyOwed(ctx.turnID) },
    { id: 'reply-length', event: 'PreToolUse', matcher: 'mcp__arizuko__reply|mcp__arizuko__send', run: (ev) => replyLength(ctx.home, ev) },
    // A skill the agent writes today runs in its NEXT session. Scans the file the
    // write would PRODUCE, not the fragment it carries (BUGS J9).
    { id: 'skill-guard', event: 'PreToolUse', matcher: 'Write|Edit|MultiEdit', run: async (ev) => skillGuard(ev) },
    // Refuses the GitHub writes a folder must not make once it holds a shell
    // credential.
    { id: 'gh-write-gate', event: 'PreToolUse', matcher: 'Bash', run: async (ev) => ghWriteGate(ev) },
    { id: 'tool-log-pre', event: 'PreToolUse', run: async (ev) => toolLogPre(ev) },
    // The catch-all. The write gate above covers claude's write TOOLS only: Bash
    // reaches the same files without them (BUGS J8), and codex writes with
    // `apply_patch`, so `Write|Edit|MultiEdit` never fires there. No matcher —
    // it rechecks the filesystem after every tool, mtime-gated.
    { id: 'skill-rescan', event: 'PostToolUse', run: async (ev) => rescan(ev) },
    { id: 'tool-log-post', event: 'PostToolUse', run: async (ev) => toolLogPost(ev) },
    { id: 'completion', event: 'PostToolUse', run: completion },
    { id: 'completion-failure', event: 'PostToolUseFailure', run: completion },
  ];
}

async function replyLength(home: string, ev: ToolEvent): Promise<Verdict> {
  const child = spawn('python3', [path.join(home, '.claude/hooks/reply_lint.py')]);
  const chunks: Buffer[] = [];
  child.stdout.on('data', (chunk: Buffer) => chunks.push(chunk));
  child.stdin.end(JSON.stringify(ev));
  const code = await new Promise<number | null>((resolve, reject) => {
    child.on('error', reject);
    child.on('close', resolve);
  });
  if (code !== 0) throw new Error(`reply lint exited ${code}`);
  const output = Buffer.concat(chunks).toString();
  if (!output) return ALLOW;
  const result = JSON.parse(output);
  return { kind: 'deny', reason: result.hookSpecificOutput.permissionDecisionReason };
}

// renderSteering is THE <user-steering> wrapper: the steering row injects it
// mid-turn, and a waiting claude session pushes it as a prompt.
export function renderSteering(messages: string[]): string {
  return `<user-steering>\n${messages.join('\n')}\n</user-steering>`;
}

// The queue is a file (spool.ts) because a closure cannot cross into codex's
// hook subprocess.
function steering(ev: ToolEvent): Verdict {
  if (ev.agent_id) return ALLOW;
  const messages = drainLines(STEER_SPOOL);
  if (messages.length === 0) return ALLOW;
  return { kind: 'inject', context: renderSteering(messages) };
}

// matches applies a row's matcher against the CANONICAL tool name. pi
// lower-cases its built-ins, so its renderer canonicalizes at the boundary.
export function matches(b: Binding, toolName: string): boolean {
  if (!b.matcher) return true;
  return new RegExp(`^(${b.matcher})$`).test(toolName);
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
// interesting tool-log lines.
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
