// pi.ts — the pi backend: drives `pi -p --mode json` (badlogic's coding
// agent) as a subprocess (spec 6/7 § "The connector seam"). Every turn is
// fresh (--no-session) — layer-1 continuity is the group layer's job. pi has
// NO MCP client: arizuko verbs never appear in its tool list, and the
// terminal frame submitted by the chassis is its only delivery. Skills load
// natively — pi implements the Agent Skills format — but only the dirs the
// load-time guard passed (skills-scan.ts).

import fs from 'fs';
import os from 'os';
import path from 'path';
import { spawn, ChildProcess } from 'child_process';
import {
  Backend,
  DrainIpcInput,
  Event,
  Session,
  SessionConfig,
  jsonLines,
} from './backend.js';
import { scanSkills } from './skills-scan.js';
import type { ModelUsage } from './mcp.js';

const HOME = '/home/node';
const QUERY_TIMEOUT_MS = Number(process.env.ARIZUKO_QUERY_TIMEOUT_MS) || 15 * 60_000;

function log(message: string): void {
  console.error(`[ant:pi] ${message}`);
}

// piArgs builds one turn's argv. The envelope travels as an @file, never
// argv — a single argv element is capped by the kernel and an envelope with
// episodes + diary can exceed it.
export function piArgs(cfg: SessionConfig, promptFile: string, skillDirs: string[]): string[] {
  const args = ['-p', '--no-session', '--mode', 'json'];
  // The agent writes $HOME, so ~/.pi extensions are agent-authored tool
  // registrations that would never cross ipc.serveConn — same self-grant
  // class strictMcpConfig closes for claude (BUGS J14). --no-skills drops
  // pi's own discovery; the explicit --skill dirs below still load.
  args.push('--no-extensions', '--no-skills');
  if (cfg.model) args.push('--model', cfg.model);
  if (typeof cfg.systemPrompt === 'string') args.push('--system-prompt', cfg.systemPrompt);
  for (const d of skillDirs) args.push('--skill', d);
  args.push(`@${promptFile}`);
  return args;
}

interface PiUsage {
  input?: number;
  output?: number;
  cacheRead?: number;
  cacheWrite?: number;
  cost?: { total?: number };
}

interface PiMessage {
  role?: string;
  content?: Array<{ type?: string; text?: string }>;
  provider?: string;
  model?: string;
  usage?: PiUsage;
  stopReason?: string;
  errorMessage?: string;
}

// PiTurn reduces pi's --mode json event stream to Events. Recorded stream
// shape (pi 0.51.6, fixtures 2026-08-20): session {id} · agent_start ·
// turn_start · message_start/message_end {message} · turn_end · agent_end,
// with auto_retry_start/auto_retry_end wrapping repeated attempts on a
// provider error. agent_end fires once PER ATTEMPT, so the terminal is the
// process exit, not any stream event. Pure so fixture tests cover the
// mapping without spawning the CLI.
export class PiTurn {
  private lastText: string | undefined;
  private lastStop: string | undefined;
  private lastError: string | undefined;
  private lastModels: Record<string, ModelUsage> | undefined;

  push(obj: Record<string, unknown>): Event[] {
    const type = obj.type as string | undefined;
    if (type === 'session') {
      return [{ type: 'system_init', raw: obj, sessionId: obj.id as string | undefined }];
    }
    if (type !== 'message_end') return [];
    const m = obj.message as PiMessage | undefined;
    if (m?.role !== 'assistant') return [];
    this.lastStop = m.stopReason;
    if (m.stopReason === 'error') {
      this.lastError = m.errorMessage?.trim() || 'pi error';
      return [];
    }
    if (m.usage) {
      const key = `${m.provider ?? 'pi'}/${m.model ?? 'unknown'}`;
      this.lastModels = { [key]: usageOf(m.usage) };
    }
    const text = (m.content ?? [])
      .filter((p) => p.type === 'text')
      .map((p) => p.text ?? '')
      .join('');
    if (!text) return [];
    this.lastText = text;
    return [{ type: 'assistant', raw: obj, text }];
  }

  // finish synthesises the single terminal result at process exit: exactly
  // one result per turn, never silence. A run whose final assistant message
  // errored (provider down, auth) is an error even on exit code 0 — pi exits
  // 0 after a failed retry loop.
  finish(exitCode: number | null, stderrTail: string): Event {
    const ok = exitCode === 0
      && this.lastText !== undefined
      && this.lastStop !== 'error'
      && this.lastStop !== 'aborted';
    const err = this.lastError ?? (stderrTail.trim() || `pi exited ${exitCode}`);
    return {
      type: 'result',
      raw: { type: 'result', exit_code: exitCode },
      text: ok ? this.lastText : err,
      final: true,
      status: ok ? 'success' : 'error',
      models: ok ? this.lastModels : undefined,
    };
  }
}

// usageOf maps pi's camelCase usage onto the submit_turn shape. cost.total is
// dollars; cost_cents is × 100 rounded (same rule as the claude backend).
function usageOf(u: PiUsage): ModelUsage {
  return {
    input: u.input ?? 0,
    output: u.output ?? 0,
    cache_read: u.cacheRead ?? 0,
    cache_write: u.cacheWrite ?? 0,
    cost_cents: Math.round((u.cost?.total ?? 0) * 100),
  };
}

class PiSession implements Session {
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

    const promptFile = path.join(os.tmpdir(), `ant-pi-prompt-${process.pid}.md`);
    fs.writeFileSync(promptFile, cfg.prompt);

    const child = spawn('pi', piArgs(cfg, promptFile, scan.clean.map((s) => s.dir)), {
      env,
      cwd: cfg.cwd ?? HOME,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    this.child = child;
    let stderrTail = '';
    child.stderr?.setEncoding('utf8');
    child.stderr?.on('data', (chunk: string) => {
      stderrTail = (stderrTail + chunk).slice(-2000);
    });

    const timer = setTimeout(() => {
      log(`query timeout (${QUERY_TIMEOUT_MS}ms) reached, killing pi`);
      this.timedOut = true;
      child.kill('SIGTERM');
    }, QUERY_TIMEOUT_MS);

    const turn = new PiTurn();
    try {
      for await (const obj of jsonLines(child.stdout!)) {
        for (const ev of turn.push(obj)) yield ev;
      }
      const exitCode = await exited(child);
      if (this.timedOut) {
        yield {
          type: 'result',
          raw: { type: 'result', timed_out: true },
          text: 'Hit the time limit on this turn — narrow the request or ask me to continue.',
          final: true,
          status: 'success',
          timedOut: true,
        };
      } else {
        yield turn.finish(exitCode, stderrTail);
      }
    } finally {
      clearTimeout(timer);
      if (child.exitCode === null) child.kill('SIGKILL');
      try { fs.unlinkSync(promptFile); } catch { /* already gone */ }
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

export const piBackend: Backend = {
  name: 'pi',
  // Fresh every turn (--no-session): pi sessions are never resumed. The
  // session id still flows back through system_init for lineage rows.
  resolveResume: () => undefined,
  isSessionError: () => false,
  spawn: (cfg: SessionConfig, _drain: DrainIpcInput) => new PiSession(cfg),
};
