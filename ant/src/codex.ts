// codex.ts — the codex harness: drives `codex exec --json` as a subprocess
// (spec 6/16). codex resumes layer 1 natively: `codex exec resume <thread_id>`
// continues a rollout under CODEX_HOME, verified against codex-cli 0.147.0.
// arizuko tools reach it through its native MCP client — turn.tools is rendered
// into -c mcp_servers.* overrides, so every call still crosses the gated socket.
// Skills arrive as a prompt index (skills-scan.ts).

import {
  driveCli,
  Frame,
  Harness,
  ResultFrame,
  Turn,
} from './backend.js';
import { renderSkillsIndex, scanSkills } from './skills-scan.js';
import { Binding, PolicyEvent, Verdict, bindings, runnableBy } from './policy.js';
import { POLICY_LOG_SPOOL, drainLines } from './spool.js';
import fs from 'fs';
import path from 'path';
import type { ModelUsage } from './mcp.js';
import { GATEWAY_KEY_ENV, modelGateway } from './gateway.js';

function log(message: string): void {
  console.error(`[ant:codex] ${message}`);
}

// tomlValue renders one -c override value. codex parses the value side as TOML
// and falls back to a literal string on parse failure, so an object must be a
// TOML inline table — JSON's `:` would silently become a literal string.
export function tomlValue(v: string | string[] | Record<string, string>): string {
  if (typeof v === 'string') return JSON.stringify(v);
  if (Array.isArray(v)) return `[${v.map((s) => JSON.stringify(s)).join(', ')}]`;
  const entries = Object.entries(v).map(([k, val]) => `${JSON.stringify(k)} = ${JSON.stringify(val)}`);
  return `{${entries.join(', ')}}`;
}

// codexArgs builds one turn's argv. The prompt travels on stdin (`-`), never
// argv — an envelope with episodes + diary can exceed the per-arg kernel cap.
// The continuity token IS codex's thread id.
export function codexArgs(turn: Pick<Turn, 'cwd' | 'model' | 'continuity' | 'tools' | 'env'>): string[] {
  const args = turn.continuity ? ['exec', 'resume'] : ['exec'];
  args.push(
    // The container is the perimeter (Docker + egress proxy + gated socket); codex's
    // own bwrap sandbox fails on no-userns kernels and guards nothing extra.
    '--dangerously-bypass-approvals-and-sandbox',
    '--skip-git-repo-check',
    // The agent writes $HOME, so $HOME/.codex/config.toml is agent-authored config —
    // an [mcp_servers] block there would be dialled straight from codex, never
    // crossing ipc.serveConn (BUGS J14). auth still resolves from CODEX_HOME.
    '--ignore-user-config',
    // Without this the hooks in $CODEX_HOME/hooks.json do not run and codex prints
    // NOTHING — no warning, no error (measured 2026-08-21).
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
  // The gateway pair (gateway.ts) becomes a model provider named `gateway`.
  // Measured 2026-09-04 with codex-cli 0.153.2: `wire_api=chat` is rejected
  // outright as "no longer supported", so it is `responses`. With a key, codex reads
  // it from the named env var; without one, requires_openai_auth makes codex
  // send its own ChatGPT bearer to base_url instead.
  const gw = modelGateway(turn.env);
  if (gw) {
    args.push('-c', `model_provider=${tomlValue('gateway')}`);
    args.push('-c', `model_providers.gateway.name=${tomlValue('gateway')}`);
    args.push('-c', `model_providers.gateway.base_url=${tomlValue(`${gw.url}/v1`)}`);
    args.push('-c', `model_providers.gateway.wire_api=${tomlValue('responses')}`);
    if (gw.key) args.push('-c', `model_providers.gateway.env_key=${tomlValue(GATEWAY_KEY_ENV)}`);
    else args.push('-c', 'model_providers.gateway.requires_openai_auth=true');
  }
  // `codex exec resume` REJECTS --cd ("unexpected argument '--cd' found"): the
  // rollout records its own working root. The child is spawned with cwd either way.
  if (turn.continuity) args.push(turn.continuity);
  else args.push('--cd', turn.cwd);
  args.push('-');
  return args;
}

// codexPrompt joins the system text, the skills index and the envelope. The
// envelope itself is untouched — it IS the contract.
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
// shape (codex-cli 0.147.0): thread.started {thread_id} · turn.started ·
// item.completed {item:{type,text}} · turn.completed {usage}. Pure, so fixture
// tests cover the mapping without spawning the CLI.
//
// RESUME_MISS matches codex's stderr when the requested rollout is gone.
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
      // The thread id is both the platform session id and the whole continuity token.
      return id ? [{ kind: 'continuity', token: id, sessionId: id }] : [];
    }
    if (type === 'item.completed') {
      const item = obj.item as { type?: string; text?: string } | undefined;
      if (item?.type === 'agent_message' && item.text) {
        this.lastText = item.text;
        // The chassis extracts <status> blocks from this text — before the seam
        // inversion it re-parsed claude's raw shape, so codex streamed no interim status.
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
      // turn.failed carries {error:{message}}; dropping it would deliver an error
      // result with no diagnostic when no assistant text preceded it.
      const err = (obj.error as { message?: string } | undefined)?.message ?? this.lastError;
      return [{ kind: 'result', ok: false, text: this.lastText ?? err }];
    }
    if (type === 'error') {
      this.lastError = (obj.message as string | undefined) ?? 'codex error';
      return [];
    }
    return [];
  }

  // finish covers a stream that ended without a terminal event (auth failure,
  // crash, a resume target that is gone): the turn still produces exactly one
  // result.
  //
  // A missing rollout is the one case that must NOT be delivered. codex writes
  // `no rollout found for thread id <id>` to stderr, emits nothing on stdout and
  // **exits 0**, so the exit code cannot be the signal.
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

// codex reports no cost; tokens land, cost_cents stays 0 — honest, not derived.
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
    // codex swallows a hook's stderr, so the policies spool their log lines to a
    // file instead. Drain as frames arrive: near-live, and no line is lost.
    for (const line of drainLines(POLICY_LOG_SPOOL)) console.error(line);
    yield frame;
  }
  for (const line of drainLines(POLICY_LOG_SPOOL)) console.error(line);
}

// POLICY_CLI is where entrypoint.sh compiles ant to, then makes read-only. A
// constant on purpose: the agent cannot edit the code behind its own gates.
const POLICY_CLI = '/tmp/dist/policy-cli.js';

// verdictToCodexJson is the ONE place a Verdict becomes codex's wire shape.
// codex reuses Claude Code's hook JSON verbatim for the two verdicts it supports.
export function verdictToCodexJson(verdict: Verdict, event: PolicyEvent): string {
  switch (verdict.kind) {
    case 'deny':
      if (event === 'Stop') return JSON.stringify({ decision: 'block', reason: verdict.reason });
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
      // Unreachable: codexRows drops any row that may rewrite.
      return '{}';
    case 'allow':
      return '{}';
  }
}

// CODEX_HONOURS is what codex can actually DO with a verdict: refuse the call
// and inject context after it. Not rewrite — that belongs to a different event.
export const CODEX_HONOURS: Array<Verdict['kind']> = ['deny', 'inject'];

export function codexRows(rows: Binding[]): Binding[] {
  return runnableBy(rows, CODEX_HONOURS, log);
}

// codexHooksConfig renders the binding table into codex's hooks.json: ONE
// command per event, which loops the rows itself. One command per ROW would
// spawn a node process per row per tool call.
//
// The schema is Claude Code's, minus a version key — measured against codex-cli
// 0.147.0, which rejects any other top-level field. No matcher is emitted: the
// rows carry their own, and policy.ts applies them.
export function codexHooksConfig(rows: Binding[], turnID: string): unknown {
  const events = [...new Set(codexRows(rows).map((b) => b.event))];
  const hooks: Record<string, unknown[]> = {};
  for (const event of events) {
    hooks[event] = [{ hooks: [{ type: 'command', command: `node ${POLICY_CLI} ${event} ${turnID}`, timeout: 30 }] }];
  }
  return { description: 'arizuko policy (spec 6/16) — generated per turn, do not edit', hooks };
}

// codexHomeDir resolves where codex will LOOK for hooks.json. Writing to
// <home>/.codex would be wrong the moment an operator sets CODEX_HOME, and the
// failure is silent: codex reads a file that is not there and runs with no gates.
export function codexHomeDir(home: string, env: Record<string, string>): string {
  return env.CODEX_HOME || path.join(home, '.codex');
}

// writeCodexHooks puts the system layer in place before codex starts. Rewriting
// it every turn is what makes it authoritative: codex reads the file when its
// process starts, so an agent edit changes nothing for the turn it lands in.
export function writeCodexHooks(home: string, turnID: string, env: Record<string, string> = {}): void {
  const dir = codexHomeDir(home, env);
  const file = path.join(dir, 'hooks.json');
  const config = codexHooksConfig(bindings({ home, turnID }), turnID);
  try {
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(file, JSON.stringify(config, null, 2));
  } catch (err) {
    // THROW, never log-and-continue. An unwritable hooks.json means the turn would
    // run with no guard, no tool log and no steering, and look healthy doing it.
    throw new Error(`cannot write ${file}: ${err} — refusing to run the turn with no policy gates`);
  }
}

export const codexHarness: Harness = {
  name: 'codex',
  // continuity 'native': the stored thread id resumes verbatim — an id minted by
  // another backend fails as a missing rollout and retries fresh.
  profile: {
    continuity: 'native',
    tools: 'mcp',
    guard: 'load-time',
    steering: 'between-turn',
    // rendered: codex looks for AGENTS.md, never ~/.claude/CLAUDE.md, so the
    // manual reaches it only through the system text codexPrompt prepends.
    manual: 'rendered',
    status: 'streaming',
  },
  run: runCodex,
};
