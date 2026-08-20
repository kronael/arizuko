// codex.ts — the codex backend: drives `codex exec --json` as a subprocess
// (spec 6/7 § "The connector seam"). Every turn is fresh — layer-1 continuity
// is the group layer's job, and the contract requires correctness with an
// empty transcript. arizuko tools reach codex through its native MCP client:
// cfg.mcpServers (the single declaration, mcp-servers.ts) is rendered into
// -c mcp_servers.* overrides, so every tool call still crosses the gated
// socket. Skills arrive as a prompt index (skills-scan.ts) — codex has no
// native skill mechanism.

import { spawn, ChildProcess } from 'child_process';
import {
  Backend,
  DrainIpcInput,
  Event,
  Session,
  SessionConfig,
  jsonLines,
} from './backend.js';
import { renderSkillsIndex, scanSkills } from './skills-scan.js';
import type { ModelUsage } from './mcp.js';

const HOME = '/home/node';
const QUERY_TIMEOUT_MS = Number(process.env.ARIZUKO_QUERY_TIMEOUT_MS) || 15 * 60_000;

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
export function codexArgs(cfg: SessionConfig): string[] {
  const args = [
    'exec',
    // The container is the perimeter (Docker + egress proxy + gated socket);
    // codex's own bwrap sandbox fails on no-userns kernels and guards nothing
    // the platform doesn't already.
    '--dangerously-bypass-approvals-and-sandbox',
    // No rollout files: the group home persists, and a per-turn session dump
    // would grow it forever.
    '--ephemeral',
    '--skip-git-repo-check',
    // The agent writes $HOME, so $HOME/.codex/config.toml is agent-authored
    // config — an [mcp_servers] block there would be dialled straight from
    // codex, never crossing ipc.serveConn. Same self-grant class strictMcpConfig
    // closes for claude (BUGS J14); auth still resolves from CODEX_HOME.
    '--ignore-user-config',
    '--json',
    '--cd', cfg.cwd ?? HOME,
  ];
  if (cfg.model) args.push('-m', cfg.model);
  for (const [name, s] of Object.entries(cfg.mcpServers ?? {})) {
    args.push('-c', `mcp_servers.${name}.command=${tomlValue(s.command)}`);
    args.push('-c', `mcp_servers.${name}.args=${tomlValue(s.args ?? [])}`);
    if (s.env && Object.keys(s.env).length > 0) {
      args.push('-c', `mcp_servers.${name}.env=${tomlValue(s.env)}`);
    }
  }
  args.push('-');
  return args;
}

// codexPrompt joins the system text, the load-time-scanned skills index and
// the envelope. The envelope itself is untouched — it IS the contract.
export function codexPrompt(cfg: SessionConfig, skillsIndex: string): string {
  const parts: string[] = [];
  if (typeof cfg.systemPrompt === 'string') parts.push(cfg.systemPrompt);
  if (skillsIndex) parts.push(skillsIndex);
  parts.push(cfg.prompt);
  return parts.join('\n\n');
}

interface CodexUsage {
  input_tokens?: number;
  cached_input_tokens?: number;
  cache_write_input_tokens?: number;
  output_tokens?: number;
}

// CodexTurn reduces the `codex exec --json` JSONL stream to Events. Recorded
// stream shape (codex-cli 0.147.0, fixture 2026-08-20): thread.started
// {thread_id} · turn.started · item.completed {item:{type,text}} ·
// turn.completed {usage}. A state machine, pure so the fixture tests cover
// the load-bearing mapping without spawning the CLI.
export class CodexTurn {
  private lastText: string | undefined;
  private lastError: string | undefined;
  private terminal = false;

  get done(): boolean {
    return this.terminal;
  }

  push(obj: Record<string, unknown>): Event[] {
    const type = obj.type as string | undefined;
    if (type === 'thread.started') {
      const id = obj.thread_id as string | undefined;
      return [{ type: 'system_init', raw: obj, sessionId: id }];
    }
    if (type === 'item.completed') {
      const item = obj.item as { type?: string; text?: string } | undefined;
      if (item?.type === 'agent_message' && item.text) {
        this.lastText = item.text;
        return [{ type: 'assistant', raw: obj, text: item.text }];
      }
      return [];
    }
    if (type === 'turn.completed') {
      this.terminal = true;
      const usage = obj.usage as CodexUsage | undefined;
      return [{
        type: 'result',
        raw: obj,
        text: this.lastText,
        final: true,
        status: 'success',
        models: usage ? { codex: usageOf(usage) } : undefined,
      }];
    }
    if (type === 'turn.failed') {
      this.terminal = true;
      return [{ type: 'result', raw: obj, text: this.lastText, final: true, status: 'error' }];
    }
    if (type === 'error') {
      this.lastError = (obj.message as string | undefined) ?? 'codex error';
      return [];
    }
    return [];
  }

  // finish covers a stream that ended without a terminal event (auth failure,
  // crash): the turn still produces exactly one result — never silence.
  finish(exitCode: number | null, stderrTail: string): Event | null {
    if (this.terminal) return null;
    const ok = exitCode === 0 && this.lastText !== undefined;
    const err = this.lastError ?? (stderrTail.trim() || `codex exited ${exitCode}`);
    return {
      type: 'result',
      raw: { type: 'result', exit_code: exitCode },
      text: ok ? this.lastText : err,
      final: true,
      status: ok ? 'success' : 'error',
    };
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

class CodexSession implements Session {
  private child: ChildProcess | undefined;
  private timedOut = false;

  constructor(private cfg: SessionConfig) {}

  async *events(): AsyncIterable<Event> {
    const cfg = this.cfg;
    const scan = scanSkills(`${cfg.cwd ?? HOME}/.claude/skills`);
    for (const x of scan.excluded) log(`skill excluded by load-time guard: ${x}`);

    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(cfg.env ?? process.env)) {
      if (v !== undefined) env[k] = v;
    }

    const child = spawn('codex', codexArgs(cfg), {
      env,
      cwd: cfg.cwd ?? HOME,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.child = child;
    let stderrTail = '';
    child.stderr?.setEncoding('utf8');
    child.stderr?.on('data', (chunk: string) => {
      stderrTail = (stderrTail + chunk).slice(-2000);
    });
    child.stdin?.end(codexPrompt(cfg, renderSkillsIndex(scan.clean)));

    const timer = setTimeout(() => {
      log(`query timeout (${QUERY_TIMEOUT_MS}ms) reached, killing codex`);
      this.timedOut = true;
      child.kill('SIGTERM');
    }, QUERY_TIMEOUT_MS);

    const turn = new CodexTurn();
    try {
      for await (const obj of jsonLines(child.stdout!)) {
        for (const ev of turn.push(obj)) yield ev;
        if (turn.done) break;
      }
      const exitCode = await exited(child);
      if (this.timedOut && !turn.done) {
        yield {
          type: 'result',
          raw: { type: 'result', timed_out: true },
          text: 'Hit the time limit on this turn — narrow the request or ask me to continue.',
          final: true,
          status: 'success',
          timedOut: true,
        };
      } else {
        const tail = turn.finish(exitCode, stderrTail);
        if (tail) yield tail;
      }
    } finally {
      clearTimeout(timer);
      if (child.exitCode === null) child.kill('SIGKILL');
    }
  }

  async close(): Promise<void> {
    this.child?.kill('SIGTERM');
  }
}

function exited(child: ChildProcess): Promise<number | null> {
  if (child.exitCode !== null) return Promise.resolve(child.exitCode);
  return new Promise((resolve) => child.once('close', (code) => resolve(code)));
}

export const codexBackend: Backend = {
  name: 'codex',
  // Fresh every turn: codex session ids are not resumed (layer 1 is
  // disposable by contract). The thread_id still flows back through
  // system_init so routd's lineage rows stay truthful.
  resolveResume: () => undefined,
  isSessionError: () => false,
  spawn: (cfg: SessionConfig, _drain: DrainIpcInput) => new CodexSession(cfg),
};
