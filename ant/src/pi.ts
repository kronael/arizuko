// pi harness and native MCP configuration.

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
import type { McpServerConfig } from './mcp-servers.js';

// The image owns the extension, outside the agent's writable home.
const PI_EXTENSION = '/app/src/pi-extension.ts';

function log(message: string): void {
  console.error(`[ant:pi] ${message}`);
}

// sessionDir is where pi keeps this group's rollouts. It sits under the group
// home, which runed bind-mounts, so the file outlives the per-turn container. pi
// creates the directory itself.
export function sessionDir(cwd: string): string {
  return path.join(cwd, '.pi', 'sessions');
}

// piArgs builds one turn's argv. The envelope travels as an @file, never argv.
//
// Continuity is pi's own session id, resolved against OUR --session-dir. The
// spec preferred handing pi a path — but pi names a new rollout
// `<timestamp>_<id>.jsonl` and the timestamp is not reconstructible, so a path
// cannot be derived from an id we were given.
export function piArgs(
  turn: Pick<Turn, 'cwd' | 'model' | 'effort' | 'systemPrompt' | 'continuity'>,
  promptFile: string,
  skillDirs: string[],
): string[] {
  const args = ['-p', '--mode', 'json'];
  // --session-dir on EVERY turn, resume or not: it is what a bare id is resolved
  // against, and on a fresh turn it is where the new rollout lands.
  args.push('--session-dir', sessionDir(turn.cwd));
  if (turn.continuity) args.push('--session', turn.continuity);
  // The agent writes $HOME, so ~/.pi extensions are agent-authored tool
  // registrations that would never cross ipc.serveConn (BUGS J14). --no-skills
  // drops pi's own discovery; the explicit --skill dirs below still load.
  args.push('--no-extensions', '--no-skills', '--no-approve', '--offline');
  args.push('-e', 'builtin:mcp');
  // Discovery is off, but an explicit -e path still loads: arizuko's policy rows
  // run, and nothing the agent wrote under ~/.pi does.
  args.push('-e', PI_EXTENSION);
  if (turn.model) args.push('--model', turn.model);
  if (turn.effort) args.push('--thinking', turn.effort);
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
// session dir. pi prints it and exits 1.
const RESUME_MISS = /No session found matching/;

// PiTurn reduces pi's --mode json event stream to frames. Recorded shape (pi
// 0.51.6): session {id} · agent_start · turn_start · message_start/message_end
// {message} · turn_end · agent_end, with auto_retry_* wrapping repeated
// attempts. agent_end fires once PER ATTEMPT, so the terminal is the process
// exit, not any stream event.
export class PiTurn {
  private lastText: string | undefined;
  private completed = false;
  private textParts: string[] = [];
  private lastStop: string | undefined;
  private lastError: string | undefined;
  private models: Record<string, ModelUsage> | undefined;

  // pi never terminates on a stream event — the process exit is the terminal.
  readonly done = false;

  push(obj: Record<string, unknown>): Frame[] {
    const type = obj.type as string | undefined;
    if (type === 'session') {
      const id = obj.id as string | undefined;
      // The id is both the continuity token and the lineage session id.
      return id ? [{ kind: 'continuity', token: id, sessionId: id }] : [];
    }
    if (type === 'message_start' && (obj.message as PiMessage | undefined)?.role === 'assistant') this.textParts = [];
    if (type === 'message_update') {
      const event = obj.assistantMessageEvent as { type?: string; contentIndex?: number; delta?: string } | undefined;
      if (event?.type === 'text_delta' && typeof event.contentIndex === 'number') {
        this.textParts[event.contentIndex] = (this.textParts[event.contentIndex] ?? '') + (event.delta ?? '');
        return [{ kind: 'status', text: this.textParts.join('') }];
      }
    }
    if (type !== 'message_end') return [];
    const m = obj.message as PiMessage | undefined;
    if (m?.role !== 'assistant') return [];
    this.lastStop = m.stopReason;
    this.completed = true;
    if (m.stopReason === 'error') {
      this.lastError = m.errorMessage?.trim() || 'pi error';
      return [];
    }
    if (m.usage) {
      // Each assistant message carries that ONE model call's usage, so the turn total
      // is the sum — keeping only the last message dropped every earlier call.
      const key = `${m.provider ?? 'pi'}/${m.model ?? 'unknown'}`;
      this.models ??= {};
      this.models[key] = addUsage(this.models[key], usageOf(m.usage));
    }
    const text = (m.content ?? [])
      .filter((p) => p.type === 'text')
      .map((p) => p.text ?? '')
      .join('');
    this.lastText = text;
    if (!text) return [];
    return [{ kind: 'status', text }];
  }

  // finish synthesises the single terminal result at process exit. A run whose
  // final assistant message errored is an error even on exit code 0 — pi exits 0
  // after a failed retry loop.
  finish(exitCode: number | null, stderrTail: string): ResultFrame {
    // A missing rollout must NOT be delivered: the chassis retries the turn fresh
    // instead of replying with a harness diagnostic.
    if (RESUME_MISS.test(stderrTail)) {
      return { kind: 'result', ok: false, retryFresh: true };
    }
    const ok = exitCode === 0
      && this.completed
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

// Cents are rounded once, on the SUM — rounding each message first zeroes a turn
// of sub-cent calls.
function roundCosts(models: Record<string, ModelUsage>): Record<string, ModelUsage> {
  const out: Record<string, ModelUsage> = {};
  for (const [k, u] of Object.entries(models)) {
    out[k] = { ...u, cost_cents: Math.round(u.cost_cents) };
  }
  return out;
}

// usageOf maps pi's camelCase usage onto the submit_turn shape. cost.total is
// dollars; cost_cents stays fractional here and is rounded once on the sum.
function usageOf(u: PiUsage): ModelUsage {
  return {
    input: u.input ?? 0,
    output: u.output ?? 0,
    cache_read: u.cacheRead ?? 0,
    cache_write: u.cacheWrite ?? 0,
    cost_cents: (u.cost?.total ?? 0) * 100,
  };
}

export function piMcpConfig(tools: Record<string, McpServerConfig>) {
  return { mcpServers: Object.fromEntries(Object.entries(tools).map(([name, server]) => [
    name, { command: server.command, args: server.args ?? [], exposure: 'direct' },
  ])) };
}

export function piEnv(env: Record<string, string>): Record<string, string> {
  const result = { ...env };
  if (!env.ANTHROPIC_OAUTH_TOKEN && env.CLAUDE_CODE_OAUTH_TOKEN) result.ANTHROPIC_OAUTH_TOKEN = env.CLAUDE_CODE_OAUTH_TOKEN;
  if (!env.OPENAI_API_KEY && env.CODEX_API_KEY) result.OPENAI_API_KEY = env.CODEX_API_KEY;
  return result;
}

// Only operator-mounted files can supply native pi configuration or login.
function readMountedFile(file: string): string | undefined {
  if (!fs.existsSync(file)) return undefined;
  const real = fs.realpathSync(file);
  if (real !== file) throw new Error(`pi source is a symlink: ${file}`);
  const mounts = fs.readFileSync('/proc/self/mountinfo', 'utf8').split('\n').map((line) => {
    const fields = line.split(' ');
    return { target: fields[4]?.replace(/\\040/g, ' '), options: fields[5]?.split(',') ?? [] };
  }).filter((m) => m.target && (real === m.target || real.startsWith(`${m.target}/`)));
  mounts.sort((a, b) => b.target.length - a.target.length);
  if (!mounts[0]?.options.includes('ro')) throw new Error(`pi source is not on a read-only mount: ${file}`);
  return fs.readFileSync(real, 'utf8');
}

export function codexAuth(source: string): Record<string, unknown> {
  const data = JSON.parse(source);
  if (typeof data.OPENAI_API_KEY === 'string' && data.OPENAI_API_KEY) {
    return { openai: { type: 'api_key', key: data.OPENAI_API_KEY } };
  }
  const { access_token: access, refresh_token: refresh, account_id: accountId } = data.tokens ?? {};
  if (typeof access !== 'string' || typeof refresh !== 'string' || typeof accountId !== 'string') throw new Error('invalid Codex login');
  const payload = JSON.parse(Buffer.from(access.split('.')[1] ?? '', 'base64url').toString());
  if (typeof payload.exp !== 'number') throw new Error('invalid Codex login expiry');
  return { 'openai-codex': { type: 'oauth', access, refresh, accountId, expires: payload.exp * 1000 } };
}

async function* runPi(turn: Turn): AsyncGenerator<Frame> {
  const scan = scanSkills(`${turn.cwd}/.claude/skills`);
  for (const x of scan.excluded) log(`skill excluded by load-time guard: ${x}`);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ant-pi-'));
  const promptFile = path.join(dir, 'prompt.md');
  try {
    const models = readMountedFile('/mnt/pi/models.json');
    if (models) {
      const parsed = JSON.parse(models);
      if (turn.env.MODEL_GATEWAY_URL && Object.keys(parsed.providers ?? {}).length) {
        throw new Error('pi models configuration cannot override the model gateway');
      }
      fs.writeFileSync(path.join(dir, 'models.json'), JSON.stringify(parsed), { mode: 0o600 });
    }
    if (!turn.env.MODEL_GATEWAY_URL) {
      const login = readMountedFile(path.join(turn.cwd, '.codex', 'auth.json'));
      if (login) fs.writeFileSync(path.join(dir, 'auth.json'), JSON.stringify(codexAuth(login)), { mode: 0o600 });
    }
    fs.writeFileSync(promptFile, turn.prompt, { mode: 0o600 });
    fs.writeFileSync(path.join(dir, 'mcp.json'), JSON.stringify(piMcpConfig(turn.tools ?? {})), { mode: 0o600 });
    fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify({ retry: {
      enabled: true, maxRetries: 2, baseDelayMs: 2000, maxAgentDelayMs: 10000, provider: { maxRetries: 0 },
    } }), { mode: 0o600 });
    yield* driveCli(
      'pi',
      piArgs(turn, promptFile, scan.clean.map((s) => s.dir)),
      { cwd: turn.cwd, env: { ...piEnv(turn.env), PI_CODING_AGENT_DIR: dir }, signal: turn.signal, deadline: turn.deadline },
      new PiTurn(),
      log,
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

export const piHarness: Harness = {
  name: 'pi',
  profile: {
    // continuity 'native': the stored id resumes against this group's own session
    // dir — an id minted by another backend makes pi exit 1, and finish() turns that
    // into a fresh retry.
    continuity: 'native',
    tools: 'mcp',
    // write-time: the extension's tool_call handler blocks a dangerous write before
    // it lands; the load-time scan in runPi stays as the backstop.
    guard: 'write-time',
    steering: 'between-turn',
    // rendered: pi has no manual convention of its own; the chassis hands it
    // over with --system-prompt.
    manual: 'rendered',
    status: 'streaming',
  },
  run: runPi,
};
