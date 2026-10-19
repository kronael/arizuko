// backend.ts — the harness seam (spec 6/16), stated as arizuko's REQUIREMENTS of
// an occupant rather than one SDK's options object: Turn in, Frames out, one
// run() per harness, a profile declaring capabilities as data. One file per
// harness (claude.ts, codex.ts, pi.ts). The outer contract stays the process
// boundary: envelope on stdin, tools on the gated MCP socket, submit_turn.

import { spawn, ChildProcess } from 'child_process';
import type { Readable } from 'stream';
import type { ModelUsage } from './mcp.js';
import type { McpServerConfig } from './mcp-servers.js';
import { claudeHarness } from './claude.js';
import { codexHarness } from './codex.js';

// Turn is one arizuko turn, in chassis facts only. Anything a single harness
// needs beyond this is derived inside its own file.
export interface Turn {
  prompt: string; // the envelope, untouched — it IS the contract
  cwd: string; // the group home
  // turnID is arizuko's turn id, chassis-derived per inner-loop turn
  // (messageId:N); the 5/24 TodoWrite hook keys the live checklist by it.
  turnID: string;
  model?: string; // backend-native id, verbatim
  // effort is the reasoning depth the harness asks for: low|medium|high|xhigh|max.
  // The instance sets it (ARIZUKO_DEFAULT_EFFORT) and claude passes it to the SDK;
  // a harness whose CLI has no effort knob ignores it rather than failing.
  effort?: string;
  continuity?: string; // opaque token from the last turn; undefined = fresh
  systemPrompt?: string; // rendered text; absent = the harness's own default
  // tools is the assembled MCP server map (the single declaration). claude renders
  // it into SDK options; codex into -c mcp_servers.* overrides; pi has no MCP
  // client and cannot read it.
  tools?: Record<string, McpServerConfig>;
  env: Record<string, string>;
  // signal is the platform winding the turn down. The chassis keeps consuming frames.
  signal?: AbortSignal;
}

// Frame is one normalized harness output. Nothing downstream parses a harness
// payload — the <status> bug lived exactly there.
export type Frame =
  // continuity: token is OPAQUE — the chassis hands it to the next run() and never
  // interprets it. sessionId is carried separately because the platform is NOT
  // opaque about it: topic fork copies the transcript file named by it (the L54
  // class), so a packed token in that field would fork every topic blind.
  | { kind: 'continuity'; token: string; sessionId: string }
  // status: interim assistant text. ONE extraction site for every harness.
  | { kind: 'status'; text: string }
  | {
      kind: 'result';
      ok: boolean;
      text?: string;
      usage?: Record<string, ModelUsage>;
      // retryFresh: the resume target was unusable. Never delivered as a reply; the
      // chassis retries the turn with no continuity.
      retryFresh?: boolean;
      // timedOut marks the graceful query-timeout summary, not a normal
      // completion. routd logs a WARN.
      timedOut?: boolean;
      // terminal marks a failure a retry cannot repair — a quota or spend
      // rejection, not a flaky call. The chassis turns it into an exit code
      // runed reads as Terminal, so the turn is never re-spawned (BUGS L175).
      terminal?: boolean;
    };

// ResultFrame is the terminal variant — what a reducer's finish() synthesizes
// and the chassis delivers.
export type ResultFrame = Extract<Frame, { kind: 'result' }>;

// HarnessProfile declares how an occupant meets each requirement, as a value —
// never a flag name. A capability that disappears becomes a named value change
// instead of a feature quietly not happening.
export interface HarnessProfile {
  continuity: 'native' | 'none'; // can a turn resume the last one
  tools: 'mcp' | 'bridged' | 'none'; // how arizuko verbs arrive
  guard: 'write-time' | 'load-time' | 'none';
  steering: 'mid-turn' | 'between-turn';
  status: 'streaming' | 'terminal-only';
  // How the resident manual (~/.claude/CLAUDE.md, seeded from ant/CLAUDE.md)
  // reaches the model: 'native' when the harness reads that file itself,
  // 'rendered' when the chassis must put it in the system prompt. One file,
  // one reader per harness — never a per-harness copy (BUGS L130).
  manual: 'native' | 'rendered';
}

// Harness is the seam: one occupant. run() is one turn — it unpacks the
// continuity token, drives its process, and yields frames.
//
// There is no `steer` argument: a closure cannot cross into codex's hook
// subprocess, so the queue is a spool directory (spool.ts) and the `steering`
// policy row reaches it on every harness.
export interface Harness {
  name: string;
  profile: HarnessProfile;
  run(turn: Turn): AsyncIterable<Frame>;
}

// QUERY_TIMEOUT_MS bounds one harness run; each harness ends the turn
// gracefully at the deadline (timeoutFrame), never a silent code-1 exit.
export const QUERY_TIMEOUT_MS = Number(process.env.ARIZUKO_QUERY_TIMEOUT_MS) || 15 * 60_000;

// timeoutFrame is the one reply a timed-out turn still delivers.
export function timeoutFrame(): ResultFrame {
  return {
    kind: 'result',
    ok: true,
    timedOut: true,
  };
}

// selectHarness maps ARIZUKO_BACKEND to an occupant. An unknown value throws so
// a misconfigured spawn fails loudly instead of running the wrong harness.
export function selectHarness(name: string | undefined): Harness {
  switch (name || 'claude') {
    case 'claude': return claudeHarness;
    case 'codex': return codexHarness;
    case 'pi': throw new Error('pi cannot deliver through MCP; select claude or codex until the bridge ships');
    default:
      throw new Error(`unknown ARIZUKO_BACKEND ${JSON.stringify(name)} (claude|codex|pi)`);
  }
}

// jsonLines yields one parsed JSON object per stdout line. Unparseable lines are
// skipped (banners, partial flushes).
export async function* jsonLines(stream: Readable): AsyncGenerator<Record<string, unknown>> {
  let buf = '';
  stream.setEncoding('utf8');
  for await (const chunk of stream) {
    buf += chunk;
    let nl: number;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      try {
        yield JSON.parse(line) as Record<string, unknown>;
      } catch { /* not JSON — skip */ }
    }
  }
  const tail = buf.trim();
  if (tail) {
    try {
      yield JSON.parse(tail) as Record<string, unknown>;
    } catch { /* not JSON — skip */ }
  }
}

// CliReducer is the pure half of a subprocess harness: JSONL in, frames out, one
// synthesized terminal at process exit when the stream never produced one —
// exactly one result per turn, never silence.
export interface CliReducer {
  push(obj: Record<string, unknown>): Frame[];
  finish(exitCode: number | null, stderrTail: string): ResultFrame | null;
  readonly done: boolean;
}

// driveCli owns the process plumbing every subprocess harness shares: spawn,
// stderr tail, query timeout, abort signal, SIGKILL on the way out. The reducers
// stay per-harness because the stream shapes differ.
export async function* driveCli(
  command: string,
  args: string[],
  opts: { cwd: string; env: Record<string, string>; stdin?: string; signal?: AbortSignal },
  reducer: CliReducer,
  log: (msg: string) => void,
): AsyncGenerator<Frame> {
  const child: ChildProcess = spawn(command, args, {
    env: opts.env,
    cwd: opts.cwd,
    stdio: [opts.stdin === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'],
  });
  let stderrTail = '';
  child.stderr?.setEncoding('utf8');
  child.stderr?.on('data', (chunk: string) => {
    stderrTail = (stderrTail + chunk).slice(-2000);
  });
  // A spawn failure (the CLI missing from the image) emits 'error', which is an
  // uncaught-exception crash without a listener. Fold it into the stderr tail and
  // resolve the exit wait, since 'close' is not guaranteed after 'error'.
  const exited = new Promise<number | null>((resolve) => {
    child.once('close', (code) => resolve(code));
    child.once('error', (err) => {
      stderrTail = (stderrTail + `\n${command}: ${err.message}`).slice(-2000);
      resolve(child.exitCode);
    });
  });
  if (opts.stdin !== undefined) {
    // A child that exits before draining a large prompt EPIPEs the write.
    child.stdin?.on('error', (err) => {
      stderrTail = (stderrTail + `\nstdin: ${err.message}`).slice(-2000);
    });
    child.stdin?.end(opts.stdin);
  }

  let timedOut = false;
  const timer = setTimeout(() => {
    log(`query timeout (${QUERY_TIMEOUT_MS}ms) reached, killing ${command}`);
    timedOut = true;
    child.kill('SIGTERM');
  }, QUERY_TIMEOUT_MS);
  const onAbort = () => child.kill('SIGTERM');
  opts.signal?.addEventListener('abort', onAbort, { once: true });

  try {
    try {
      for await (const obj of jsonLines(child.stdout!)) {
        for (const f of reducer.push(obj)) yield f;
        if (reducer.done) break;
      }
    } catch (err) {
      // A failed spawn closes stdout mid-iteration (bun raises
      // ERR_STREAM_PREMATURE_CLOSE). The terminal still comes from finish().
      const msg = err instanceof Error ? err.message : String(err);
      stderrTail = (stderrTail + `\nstdout: ${msg}`).slice(-2000);
    }
    const exitCode = child.exitCode !== null ? child.exitCode : await exited;
    if (timedOut && !reducer.done) {
      yield timeoutFrame();
    } else {
      const tail = reducer.finish(exitCode, stderrTail);
      if (tail) yield tail;
    }
  } finally {
    clearTimeout(timer);
    opts.signal?.removeEventListener('abort', onAbort);
    if (child.exitCode === null) child.kill('SIGKILL');
  }
}
