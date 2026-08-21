// codex.ts — the codex harness: drives `codex exec --json` as a subprocess
// (spec 6/7 § "The harness seam"). codex resumes layer 1 natively: a turn
// persists a rollout under CODEX_HOME and `codex exec resume <thread_id>`
// continues it, verified 2026-08-20 against codex-cli 0.147.0. The group layer
// still carries continuity on its own — the contract requires correctness with
// an empty transcript — but throwing a working transcript away costs the
// provider cache every turn. arizuko tools reach codex through its native MCP
// client: turn.tools (the single declaration, mcp-servers.ts) is rendered into
// -c mcp_servers.* overrides, so every tool call still crosses the gated
// socket. Skills arrive as a prompt index (skills-scan.ts) — codex has no
// native skill mechanism.

import {
  driveCli,
  Frame,
  Harness,
  ResultFrame,
  Turn,
} from './backend.js';
import { renderSkillsIndex, scanSkills } from './skills-scan.js';
import { Binding, PolicyEvent, Verdict, bindings } from './policy.js';
import { drainLines } from './spool.js';
import { POLICY_LOG_FILE } from './policy-log.js';
import fs from 'fs';
import path from 'path';
import type { ModelUsage } from './mcp.js';

function log(message: string): void {
  console.error(`[ant:codex] ${message}`);
}

// tomlValue renders one -c override value. codex parses the value side as
// TOML and falls back to a literal string on parse failure — so an object
// must be a TOML inline table, not JSON (JSON's `:` would silently become a
// literal string).
export function tomlValue(v: string | string[] | Record<string, string>): string {
  if (typeof v === 'string') return JSON.stringify(v);
  if (Array.isArray(v)) return `[${v.map((s) => JSON.stringify(s)).join(', ')}]`;
  const entries = Object.entries(v).map(([k, val]) => `${JSON.stringify(k)} = ${JSON.stringify(val)}`);
  return `{${entries.join(', ')}}`;
}

// codexArgs builds one turn's argv. The prompt travels on stdin (`-`), never
// argv — an envelope with episodes + diary can exceed the per-arg kernel cap.
// The continuity token IS codex's thread id — codex packs nothing else into it.
export function codexArgs(turn: Pick<Turn, 'cwd' | 'model' | 'continuity' | 'tools'>): string[] {
  const args = turn.continuity ? ['exec', 'resume'] : ['exec'];
  args.push(
    // The container is the perimeter (Docker + egress proxy + gated socket);
    // codex's own bwrap sandbox fails on no-userns kernels and guards nothing
    // the platform doesn't already.
    '--dangerously-bypass-approvals-and-sandbox',
    '--skip-git-repo-check',
    // The agent writes $HOME, so $HOME/.codex/config.toml is agent-authored
    // config — an [mcp_servers] block there would be dialled straight from
    // codex, never crossing ipc.serveConn. Same self-grant class strictMcpConfig
    // closes for claude (BUGS J14); auth still resolves from CODEX_HOME.
    '--ignore-user-config',
    // Without this the hooks in $CODEX_HOME/hooks.json do not run and codex
    // prints NOTHING — no warning, no error (measured 2026-08-21). The trust
    // ledger exists to prove provenance; ant writes the file itself, one turn
    // before the process starts, which proves it better.
    '--dangerously-bypass-hook-trust',
    '--json',
  );
  if (turn.model) args.push('-m', turn.model);
  for (const [name, s] of Object.entries(turn.tools ?? {})) {
    args.push('-c', `mcp_servers.${name}.command=${tomlValue(s.command)}`);
    args.push('-c', `mcp_servers.${name}.args=${tomlValue(s.args ?? [])}`);
    if (s.env && Object.keys(s.env).length > 0) {
      args.push('-c', `mcp_servers.${name}.env=${tomlValue(s.env)}`);
    }
  }
  // `codex exec resume` REJECTS --cd ("unexpected argument '--cd' found"): the
  // rollout records its own working root. The child is spawned with cwd either
  // way, so the resumed turn still runs in the group home.
  if (turn.continuity) args.push(turn.continuity);
  else args.push('--cd', turn.cwd);
  args.push('-');
  return args;
}

// codexPrompt joins the system text, the load-time-scanned skills index and
// the envelope. The envelope itself is untouched — it IS the contract.
export function codexPrompt(
  turn: Pick<Turn, 'prompt' | 'systemPrompt'>,
  skillsIndex: string,
): string {
  const parts: string[] = [];
  if (turn.systemPrompt) parts.push(turn.systemPrompt);
  if (skillsIndex) parts.push(skillsIndex);
  parts.push(turn.prompt);
  return parts.join('\n\n');
}

interface CodexUsage {
  input_tokens?: number;
  cached_input_tokens?: number;
  cache_write_input_tokens?: number;
  output_tokens?: number;
}

// CodexTurn reduces the `codex exec --json` JSONL stream to frames. Recorded
// stream shape (codex-cli 0.147.0, fixture 2026-08-20): thread.started
// {thread_id} · turn.started · item.completed {item:{type,text}} ·
// turn.completed {usage}. A state machine, pure so the fixture tests cover
// the load-bearing mapping without spawning the CLI.
// RESUME_MISS matches codex's stderr when the requested rollout is gone —
// a folder whose stored id was minted by another backend, or a pruned home.
const RESUME_MISS = /no rollout found for thread id/;

export class CodexTurn {
  private lastText: string | undefined;
  private lastError: string | undefined;
  private terminal = false;

  get done(): boolean {
    return this.terminal;
  }

  push(obj: Record<string, unknown>): Frame[] {
    const type = obj.type as string | undefined;
    if (type === 'thread.started') {
      const id = obj.thread_id as string | undefined;
      // The thread id is both the platform session id and the whole
      // continuity token — codex resumes by it verbatim.
      return id ? [{ kind: 'continuity', token: id, sessionId: id }] : [];
    }
    if (type === 'item.completed') {
      const item = obj.item as { type?: string; text?: string } | undefined;
      if (item?.type === 'agent_message' && item.text) {
        this.lastText = item.text;
        // The chassis extracts <status> blocks from this text — before the
        // seam inversion it re-parsed claude's raw shape instead, so codex
        // streamed no interim status at all (spec 6/7, the live bug).
        return [{ kind: 'status', text: item.text }];
      }
      return [];
    }
    if (type === 'turn.completed') {
      this.terminal = true;
      const usage = obj.usage as CodexUsage | undefined;
      return [{
        kind: 'result',
        ok: true,
        text: this.lastText,
        usage: usage ? { codex: usageOf(usage) } : undefined,
      }];
    }
    if (type === 'turn.failed') {
      this.terminal = true;
      return [{ kind: 'result', ok: false, text: this.lastText }];
    }
    if (type === 'error') {
      this.lastError = (obj.message as string | undefined) ?? 'codex error';
      return [];
    }
    return [];
  }

  // finish covers a stream that ended without a terminal event (auth failure,
  // crash, a resume target that is gone): the turn still produces exactly one
  // result — never silence.
  //
  // A missing rollout is the one case that must NOT be delivered. codex writes
  // `no rollout found for thread id <id>` to stderr, emits nothing on stdout
  // and **exits 0**, so the exit code cannot be the signal. retryFresh marks
  // it: the chassis retries the turn fresh instead of replying with a harness
  // diagnostic. (Pre-seam this fabricated a claude-shaped subtype — the
  // defect spec 6/7's inversion records.)
  finish(exitCode: number | null, stderrTail: string): ResultFrame | null {
    if (this.terminal) return null;
    if (RESUME_MISS.test(stderrTail)) {
      return { kind: 'result', ok: false, retryFresh: true };
    }
    const ok = exitCode === 0 && this.lastText !== undefined;
    const err = this.lastError ?? (stderrTail.trim() || `codex exited ${exitCode}`);
    return { kind: 'result', ok, text: ok ? this.lastText : err };
  }
}

// usageOf maps codex token accounting onto the submit_turn shape. codex
// reports no cost; tokens land, cost_cents stays 0 — honest, not derived.
function usageOf(u: CodexUsage): ModelUsage {
  return {
    input: u.input_tokens ?? 0,
    output: u.output_tokens ?? 0,
    cache_read: u.cached_input_tokens ?? 0,
    cache_write: u.cache_write_input_tokens ?? 0,
    cost_cents: 0,
  };
}

async function* runCodex(turn: Turn): AsyncGenerator<Frame> {
  const scan = scanSkills(`${turn.cwd}/.claude/skills`);
  for (const x of scan.excluded) log(`skill excluded by load-time guard: ${x}`);
  writeCodexHooks(turn.cwd, turn.turnID, turn.env);
  for await (const frame of driveCli(
    'codex',
    codexArgs(turn),
    {
      cwd: turn.cwd,
      env: turn.env,
      stdin: codexPrompt(turn, renderSkillsIndex(scan.clean)),
      signal: turn.signal,
    },
    new CodexTurn(),
    log,
  )) {
    // codex swallows a hook's stderr, so the policies spool their log lines to
    // a file instead. Drain it as frames arrive: near-live, and no line is lost
    // when the turn ends.
    for (const line of drainLines(POLICY_LOG_FILE)) console.error(line);
    yield frame;
  }
  for (const line of drainLines(POLICY_LOG_FILE)) console.error(line);
}

// POLICY_CLI is where entrypoint.sh compiles ant to (`bunx tsc --outDir
// /tmp/dist`), then makes read-only. The path is a constant on purpose: the
// agent cannot edit the code behind its own gates, and codex needs a command
// line it can run without knowing anything about ant's layout.
const POLICY_CLI = '/tmp/dist/policy-cli.js';

// verdictToCodexJson is the ONE place a Verdict becomes codex's wire shape.
// codex reuses Claude Code's hook JSON verbatim for the two verdicts it
// supports, so nothing is translated but the name of the event.
export function verdictToCodexJson(verdict: Verdict, event: PolicyEvent): string {
  switch (verdict.kind) {
    case 'deny':
      return JSON.stringify({
        hookSpecificOutput: {
          hookEventName: 'PreToolUse',
          permissionDecision: 'deny',
          permissionDecisionReason: verdict.reason,
        },
      });
    case 'inject':
      return JSON.stringify({
        hookSpecificOutput: { hookEventName: event, additionalContext: verdict.context },
      });
    case 'rewrite':
      // Unreachable: codexRows drops any row that may rewrite, because codex's
      // PreToolUse decision is approve|block|allow and updatedInput belongs to
      // its separate PermissionRequest event.
      return '{}';
    case 'allow':
      return '{}';
  }
}

// codexRows is the renderer's contract check. A row codex cannot honour is
// REFUSED loudly rather than rendered into a gate that quietly does nothing —
// the failure mode this whole increment exists to remove.
export function codexRows(rows: Binding[]): Binding[] {
  return rows.filter((b) => {
    if (!b.returns.includes('rewrite')) return true;
    log(`policy ${b.id} not rendered: codex cannot honour a 'rewrite' verdict`);
    return false;
  });
}

// codexHooksConfig renders the binding table into codex's hooks.json. The
// schema is Claude Code's, minus a version key: { description, hooks: { Event:
// [{ matcher, hooks: [{ type, command, timeout }] }] } } — measured against
// codex-cli 0.147.0, which rejects any other top-level field.
export function codexHooksConfig(rows: Binding[], turnID: string): unknown {
  const hooks: Record<string, unknown[]> = {};
  for (const b of codexRows(rows)) {
    (hooks[b.event] ??= []).push({
      ...(b.matcher ? { matcher: b.matcher } : {}),
      hooks: [{ type: 'command', command: `node ${POLICY_CLI} ${b.id} ${turnID}`, timeout: 30 }],
    });
  }
  return { description: 'arizuko policy (spec 6/7) — generated per turn, do not edit', hooks };
}

// codexHomeDir resolves where codex will LOOK for hooks.json. Writing to
// <home>/.codex would be wrong the moment an operator sets CODEX_HOME, and the
// failure is silent: codex reads a file that is not there and runs with no
// gates at all.
export function codexHomeDir(home: string, env: Record<string, string>): string {
  return env.CODEX_HOME || path.join(home, '.codex');
}

// writeCodexHooks puts the system layer in place before codex starts. Rewriting
// it every turn is what makes it authoritative: codex reads the file when its
// process starts, so an agent edit changes nothing for the turn it lands in and
// is overwritten before the next one.
export function writeCodexHooks(home: string, turnID: string, env: Record<string, string> = {}): void {
  const dir = codexHomeDir(home, env);
  const file = path.join(dir, 'hooks.json');
  const config = codexHooksConfig(bindings({ home, turnID }), turnID);
  try {
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(file, JSON.stringify(config, null, 2));
  } catch (err) {
    // An unwritable hooks.json means the turn runs with NO guard, no tool log
    // and no steering. Say so — this is the exact silence the increment closes.
    console.error(`[ant:codex] FAILED to write ${file}: ${err} — this turn runs with no policy gates`);
  }
}

export const codexHarness: Harness = {
  name: 'codex',
  // continuity 'native': the stored thread id resumes verbatim — an id minted
  // by another backend fails as a missing rollout and retries fresh, the same
  // outcome a shape check would reach and one fewer place to disagree about
  // what a valid id looks like.
  profile: {
    continuity: 'native',
    tools: 'mcp',
    guard: 'load-time',
    steering: 'between-turn',
    status: 'streaming',
  },
  run: runCodex,
};
