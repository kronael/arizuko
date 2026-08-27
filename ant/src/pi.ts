// pi.ts — the pi harness: drives `pi -p --mode json` (badlogic's coding
// agent) as a subprocess (spec 6/7 § "The harness seam"). pi resumes layer 1
// natively: sessions live under the group home, so a rollout survives between
// spawns the same way codex's does. The group layer still carries continuity on
// its own — the contract requires correctness with an empty transcript — but
// throwing a working transcript away costs the provider cache every turn.
// pi has NO MCP client: arizuko verbs never appear in its tool list, and the
// terminal frame submitted by the chassis is its only delivery. Skills load
// natively — pi implements the Agent Skills format — but only the dirs the
// load-time guard passed (skills-scan.ts).

import fs from 'fs';
import os from 'os';
import path from 'path';
import {
  driveCli,
  Frame,
  Harness,
  ResultFrame,
  Turn,
} from './backend.js';
import { scanSkills } from './skills-scan.js';
import type { ModelUsage } from './mcp.js';

// PI_EXTENSION mirrors codex.ts's POLICY_CLI: entrypoint.sh compiles ant to
// /tmp/dist and makes it read-only, so the code behind the gates is not the
// agent's to edit.
const PI_EXTENSION = '/tmp/dist/pi-extension.js';

function log(message: string): void {
  console.error(`[ant:pi] ${message}`);
}

// sessionDir is where pi keeps this group's rollouts. It sits under the group
// home, which runed bind-mounts into the container, so the file outlives the
// per-turn container. pi creates the directory itself (mkdirSync in its
// SessionManager), so nothing here has to.
export function sessionDir(cwd: string): string {
  return path.join(cwd, '.pi', 'sessions');
}

// piArgs builds one turn's argv. The envelope travels as an @file, never
// argv — a single argv element is capped by the kernel and an envelope with
// episodes + diary can exceed it.
//
// Continuity is pi's own session id, resolved against OUR --session-dir. The
// spec preferred handing pi a path instead, to avoid depending on a resumable
// id — but pi names a new rollout `<timestamp>_<id>.jsonl` (session-manager.js
// :499), and the timestamp is not reconstructible, so a path cannot be derived
// from an id we were given. The id form is the one that works; the private
// --session-dir is what keeps the lookup scoped to this group.
export function piArgs(
  turn: Pick<Turn, 'cwd' | 'model' | 'systemPrompt' | 'continuity'>,
  promptFile: string,
  skillDirs: string[],
): string[] {
  const args = ['-p', '--mode', 'json'];
  // --session-dir on EVERY turn, resume or not: it is what a bare id is
  // resolved against, and on a fresh turn it is where the new rollout lands.
  args.push('--session-dir', sessionDir(turn.cwd));
  if (turn.continuity) args.push('--session', turn.continuity);
  // The agent writes $HOME, so ~/.pi extensions are agent-authored tool
  // registrations that would never cross ipc.serveConn — same self-grant
  // class strictMcpConfig closes for claude (BUGS J14). --no-skills drops
  // pi's own discovery; the explicit --skill dirs below still load.
  args.push('--no-extensions', '--no-skills');
  // Discovery is off, but an explicit -e path still loads (pi --help says so).
  // That is exactly the combination wanted: arizuko's policy rows run, and
  // nothing the agent wrote under ~/.pi does. The file sits in the read-only
  // /tmp/dist entrypoint.sh compiles ant into, beside policy-cli.js.
  args.push('-e', PI_EXTENSION);
  if (turn.model) args.push('--model', turn.model);
  if (turn.systemPrompt) args.push('--system-prompt', turn.systemPrompt);
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

// RESUME_MISS matches pi's stderr when the id resolves to no rollout in our
// session dir — a folder whose stored id was minted by another backend, or a
// pruned home. pi prints it and exits 1 (main.js:331-333).
const RESUME_MISS = /No session found matching/;

// PiTurn reduces pi's --mode json event stream to frames. Recorded stream
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
  private models: Record<string, ModelUsage> | undefined;

  // pi never terminates on a stream event — the process exit is the terminal.
  readonly done = false;

  push(obj: Record<string, unknown>): Frame[] {
    const type = obj.type as string | undefined;
    if (type === 'session') {
      const id = obj.id as string | undefined;
      // The id is both the continuity token and the lineage session id: pi
      // resumes by it verbatim against our --session-dir.
      return id ? [{ kind: 'continuity', token: id, sessionId: id }] : [];
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
      // Each assistant message carries that ONE model call's usage (the
      // recorded fixtures show it per message), so the turn total is the sum —
      // keeping only the last message dropped every earlier call's tokens.
      const key = `${m.provider ?? 'pi'}/${m.model ?? 'unknown'}`;
      this.models ??= {};
      this.models[key] = addUsage(this.models[key], usageOf(m.usage));
    }
    const text = (m.content ?? [])
      .filter((p) => p.type === 'text')
      .map((p) => p.text ?? '')
      .join('');
    if (!text) return [];
    this.lastText = text;
    return [{ kind: 'status', text }];
  }

  // finish synthesises the single terminal result at process exit: exactly
  // one result per turn, never silence. A run whose final assistant message
  // errored (provider down, auth) is an error even on exit code 0 — pi exits
  // 0 after a failed retry loop.
  finish(exitCode: number | null, stderrTail: string): ResultFrame {
    // A missing rollout must NOT be delivered: the chassis retries the turn
    // fresh instead of replying with a harness diagnostic. Same seam codex
    // uses, and the only reason pi needs it — every other pi failure is a
    // real turn failure.
    if (RESUME_MISS.test(stderrTail)) {
      return { kind: 'result', ok: false, retryFresh: true };
    }
    const ok = exitCode === 0
      && this.lastText !== undefined
      && this.lastStop !== 'error'
      && this.lastStop !== 'aborted';
    const err = this.lastError ?? (stderrTail.trim() || `pi exited ${exitCode}`);
    return {
      kind: 'result',
      ok,
      text: ok ? this.lastText : err,
      usage: ok && this.models ? roundCosts(this.models) : undefined,
    };
  }
}

function addUsage(a: ModelUsage | undefined, b: ModelUsage): ModelUsage {
  if (!a) return b;
  return {
    input: a.input + b.input,
    output: a.output + b.output,
    cache_read: a.cache_read + b.cache_read,
    cache_write: a.cache_write + b.cache_write,
    cost_cents: a.cost_cents + b.cost_cents,
  };
}

// Cents are rounded once, on the SUM — rounding each message first zeroes a
// turn of sub-cent calls.
function roundCosts(models: Record<string, ModelUsage>): Record<string, ModelUsage> {
  const out: Record<string, ModelUsage> = {};
  for (const [k, u] of Object.entries(models)) {
    out[k] = { ...u, cost_cents: Math.round(u.cost_cents) };
  }
  return out;
}

// usageOf maps pi's camelCase usage onto the submit_turn shape. cost.total is
// dollars; cost_cents stays fractional here and is rounded once on the summed
// turn total (roundCosts) — the same × 100 rule as the claude harness.
function usageOf(u: PiUsage): ModelUsage {
  return {
    input: u.input ?? 0,
    output: u.output ?? 0,
    cache_read: u.cacheRead ?? 0,
    cache_write: u.cacheWrite ?? 0,
    cost_cents: (u.cost?.total ?? 0) * 100,
  };
}

async function* runPi(turn: Turn): AsyncGenerator<Frame> {
  const scan = scanSkills(`${turn.cwd}/.claude/skills`);
  for (const x of scan.excluded) log(`skill excluded by load-time guard: ${x}`);

  const promptFile = path.join(os.tmpdir(), `ant-pi-prompt-${process.pid}.md`);
  fs.writeFileSync(promptFile, turn.prompt);
  try {
    yield* driveCli(
      'pi',
      piArgs(turn, promptFile, scan.clean.map((s) => s.dir)),
      { cwd: turn.cwd, env: turn.env, signal: turn.signal },
      new PiTurn(),
      log,
    );
  } finally {
    try { fs.unlinkSync(promptFile); } catch { /* already gone */ }
  }
}

export const piHarness: Harness = {
  name: 'pi',
  profile: {
    // continuity 'native': the stored id resumes against this group's own
    // session dir — an id minted by another backend resolves to nothing, pi
    // exits 1 saying so, and finish() turns that into a fresh retry.
    continuity: 'native',
    tools: 'none',
    // write-time: the extension's tool_call handler blocks a dangerous write
    // before it lands (pi-extension.ts); the load-time scan in runPi stays as
    // the backstop, same layering as claude.
    guard: 'write-time',
    steering: 'between-turn',
    status: 'streaming',
  },
  run: runPi,
};
