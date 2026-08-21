// pi.ts — the pi harness: drives `pi -p --mode json` (badlogic's coding
// agent) as a subprocess (spec 6/7 § "The harness seam"). Every turn is
// fresh (--no-session) — layer-1 continuity is the group layer's job; the
// profile says so (continuity: 'none') and run() ignores turn.continuity.
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

// piArgs builds one turn's argv. The envelope travels as an @file, never
// argv — a single argv element is capped by the kernel and an envelope with
// episodes + diary can exceed it.
export function piArgs(
  turn: Pick<Turn, 'model' | 'systemPrompt'>,
  promptFile: string,
  skillDirs: string[],
): string[] {
  const args = ['-p', '--no-session', '--mode', 'json'];
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
  private lastModels: Record<string, ModelUsage> | undefined;

  // pi never terminates on a stream event — the process exit is the terminal.
  readonly done = false;

  push(obj: Record<string, unknown>): Frame[] {
    const type = obj.type as string | undefined;
    if (type === 'session') {
      const id = obj.id as string | undefined;
      // Sessions are not resumed (--no-session); the id still flows to the
      // lineage row through the continuity frame's sessionId.
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
      const key = `${m.provider ?? 'pi'}/${m.model ?? 'unknown'}`;
      this.lastModels = { [key]: usageOf(m.usage) };
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
    const ok = exitCode === 0
      && this.lastText !== undefined
      && this.lastStop !== 'error'
      && this.lastStop !== 'aborted';
    const err = this.lastError ?? (stderrTail.trim() || `pi exited ${exitCode}`);
    return {
      kind: 'result',
      ok,
      text: ok ? this.lastText : err,
      usage: ok ? this.lastModels : undefined,
    };
  }
}

// usageOf maps pi's camelCase usage onto the submit_turn shape. cost.total is
// dollars; cost_cents is × 100 rounded (same rule as the claude harness).
function usageOf(u: PiUsage): ModelUsage {
  return {
    input: u.input ?? 0,
    output: u.output ?? 0,
    cache_read: u.cacheRead ?? 0,
    cache_write: u.cacheWrite ?? 0,
    cost_cents: Math.round((u.cost?.total ?? 0) * 100),
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
    continuity: 'none',
    tools: 'none',
    guard: 'load-time',
    steering: 'between-turn',
    status: 'streaming',
  },
  run: runPi,
};
