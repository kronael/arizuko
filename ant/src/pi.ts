// pi harness and native MCP configuration.

import fs from 'fs';
import os from 'os';
import path from 'path';
import {
  addUsage,
  isBudgetRefusal,
  driveCli,
  Frame,
  Harness,
  ResultFrame,
  Turn,
} from './backend.js';
import { modelGateway } from './gateway.js';
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
//
// A status frame carries each completed <status> block once: per content part it
// remembers how far the part's text has been handed on, and a frame holds the text
// from there to the end of the part's last complete </status>. The chassis keeps no
// memory of what it has submitted.
export class PiTurn {
  private lastText: string | undefined;
  private completed = false;
  private textParts: string[] = [];
  private handed: number[] = [];
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
    if (type === 'message_start' && (obj.message as PiMessage | undefined)?.role === 'assistant') this.reset();
    if (type === 'message_update') {
      const event = obj.assistantMessageEvent as { type?: string; contentIndex?: number; delta?: string } | undefined;
      if (event?.type === 'text_delta' && typeof event.contentIndex === 'number') {
        this.textParts[event.contentIndex] = (this.textParts[event.contentIndex] ?? '') + (event.delta ?? '');
        return this.completedBlocks();
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
    (m.content ?? []).forEach((p, i) => {
      if (p.type === 'text') this.textParts[i] = p.text ?? '';
    });
    const frames = this.completedBlocks();
    this.reset();
    return frames;
  }

  private reset(): void {
    this.textParts = [];
    this.handed = [];
  }

  // completedBlocks hands on, from each part, the text up to the end of its last
  // complete </status> that an earlier frame did not carry. Nothing is emitted
  // while no new block has completed.
  private completedBlocks(): Frame[] {
    let text = '';
    this.textParts.forEach((part, i) => {
      const close = part.lastIndexOf('</status>');
      const end = close + '</status>'.length;
      const from = this.handed[i] ?? 0;
      if (close < 0 || end <= from) return;
      text += part.slice(from, end);
      this.handed[i] = end;
    });
    return text ? [{ kind: 'status', text }] : [];
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
    if (!ok && this.lastStop === 'error' && this.lastError && isBudgetRefusal('', this.lastError))
      return { kind: 'result', ok, text: err, refused: 'budget' };
    return {
      kind: 'result',
      ok,
      text: ok ? this.lastText : err,
      usage: ok && this.models ? roundCosts(this.models) : undefined,
    };
  }
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

// Only operator-mounted files can supply native pi configuration or login. A file
// under a mounted directory is the agent's own to write, so it never qualifies.
// `own`: the file must itself be a mount point (read-only or read-write) — the
// operator-staged overmount of ~/.codex/auth.json. Otherwise it must sit on a
// read-only mount.
function readMountedFile(file: string, own = false): string | undefined {
  if (!fs.existsSync(file)) return undefined;
  const real = fs.realpathSync(file);
  if (real !== file) throw new Error(`pi source is a symlink: ${file}`);
  const mounts = fs.readFileSync('/proc/self/mountinfo', 'utf8').split('\n').map((line) => {
    const fields = line.split(' ');
    return { target: fields[4]?.replace(/\\040/g, ' '), options: fields[5]?.split(',') ?? [] };
  }).filter((m) => m.target && (real === m.target || real.startsWith(`${m.target}/`)));
  mounts.sort((a, b) => b.target.length - a.target.length);
  if (own) {
    if (mounts[0]?.target !== real) throw new Error(`pi login is not a mount point of its own: ${file}`);
  } else if (!mounts[0]?.options.includes('ro')) throw new Error(`pi source is not on a read-only mount: ${file}`);
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

// refreshedCodexLogin returns the Codex auth.json text carrying the tokens pi
// refreshed during the turn, or undefined when there is nothing to save. `written`
// is the pi auth ant handed pi; `piAuth` is pi's auth.json after the turn. Codex's
// own fields are kept as they are; tokens.access_token, tokens.refresh_token,
// tokens.account_id (when pi's changed) and last_refresh (RFC 3339) are replaced.
export function refreshedCodexLogin(source: string, written: Record<string, unknown>, piAuth: string, now: Date): string | undefined {
  const before = written['openai-codex'] as { access: string; refresh: string; accountId: string } | undefined;
  if (!before) return undefined;
  const after = JSON.parse(piAuth)['openai-codex'];
  if (after?.type !== 'oauth' || typeof after.access !== 'string' || !after.access
    || typeof after.refresh !== 'string' || !after.refresh) return undefined;
  if (after.access === before.access && after.refresh === before.refresh) return undefined;
  const data = JSON.parse(source);
  data.tokens.access_token = after.access;
  data.tokens.refresh_token = after.refresh;
  if (typeof after.accountId === 'string' && after.accountId && after.accountId !== before.accountId) data.tokens.account_id = after.accountId;
  data.last_refresh = now.toISOString();
  return JSON.stringify(data, null, 2);
}

async function* runPi(turn: Turn): AsyncGenerator<Frame> {
  const scan = scanSkills(`${turn.cwd}/.claude/skills`);
  for (const x of scan.excluded) log(`skill excluded by load-time guard: ${x}`);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ant-pi-'));
  const promptFile = path.join(dir, 'prompt.md');
  const loginFile = path.join(turn.cwd, '.codex', 'auth.json');
  let login: { source: string; written: Record<string, unknown> } | undefined;
  try {
    const models = readMountedFile('/mnt/pi/models.json');
    if (models) {
      const parsed = JSON.parse(models);
      const gateway = modelGateway(turn.env);
      if (gateway) {
        for (const [name, provider] of Object.entries<{ baseUrl?: unknown }>(parsed.providers ?? {})) {
          const base = provider.baseUrl;
          if (typeof base !== 'string' || (base !== gateway.url && !base.startsWith(`${gateway.url}/`)))
            throw new Error(`pi provider ${name} bypasses the model gateway`);
        }
      }
      fs.writeFileSync(path.join(dir, 'models.json'), JSON.stringify(parsed), { mode: 0o600 });
    }
    if (!turn.env.MODEL_GATEWAY_URL) {
      const source = readMountedFile(loginFile, true);
      if (source !== undefined) {
        login = { source, written: codexAuth(source) };
        fs.writeFileSync(path.join(dir, 'auth.json'), JSON.stringify(login.written), { mode: 0o600 });
      }
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
    // pi rotates the refresh token inside its agent dir; carry it back before the
    // dir goes, whether or not the turn succeeded. The file is a mount point, so
    // it is rewritten in place. Only the error code is logged, never token text.
    if (login) {
      try {
        const text = refreshedCodexLogin(login.source, login.written, fs.readFileSync(path.join(dir, 'auth.json'), 'utf8'), new Date());
        if (text !== undefined) fs.writeFileSync(loginFile, text);
      } catch (e) {
        log(`refreshed Codex token was not saved: ${(e as NodeJS.ErrnoException).code ?? 'invalid login'}`);
      }
    }
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
