// claude.ts — the claude harness (spec 6/7), and the default occupant. Drives
// Claude Code via the agent SDK's query(): model calls, tool execution, retries
// and session state all live in the harness; this module spawns it and
// normalizes its message stream into frames.

import fs from 'fs';
import os from 'os';
import path from 'path';
import {
  query,
  EffortLevel,
  HookCallback,
  PreCompactHookInput,
} from '@anthropic-ai/claude-agent-sdk';
import { Binding, PolicyEvent, ToolEvent, bindings, runRow } from './policy.js';
import type { ModelUsage } from './mcp.js';
import { QUERY_TIMEOUT_MS, timeoutFrame } from './backend.js';
import type { Frame, Harness, ResultFrame, Turn } from './backend.js';
import { modelGateway } from './gateway.js';

const HOME = '/home/node';

// A resumable Claude Code session id is a UUID. arizuko's lineage placeholders
// (sess-<nano>, fork hex) are not, and `claude --resume` rejects them.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// The continuity token is claude's own pack: a bare session UUID, or
// sessionId#resumeAt intra-run so the next inner-loop turn resumes at the last
// assistant message. '#' never appears in a UUID. The chassis never parses this.
export function packContinuity(sessionId: string, resumeAt?: string): string {
  return resumeAt ? `${sessionId}#${resumeAt}` : sessionId;
}

export function unpackContinuity(
  token: string | undefined,
): { sessionId?: string; resumeAt?: string } {
  if (!token) return {};
  const hash = token.indexOf('#');
  if (hash < 0) return { sessionId: token };
  return { sessionId: token.slice(0, hash), resumeAt: token.slice(hash + 1) };
}

// transcriptPath is the on-disk SDK transcript for a Claude session id. Every
// container mounts the group home at $HOME=/home/node, so Claude Code slugifies
// the projects dir to a fixed "-home-node" (matches runner.go CopySession).
export function transcriptPath(sessionId: string): string {
  return path.join(HOME, '.claude', 'projects', '-home-node', `${sessionId}.jsonl`);
}

// resolveResumeSession maps a stored session id to what `--resume` should get,
// returning undefined ("start fresh") when the id is not resumable: a non-UUID
// arizuko placeholder, or a UUID whose transcript is absent on disk. Resuming
// the latter returns "No conversation found" → error_during_execution and silent
// context loss.
export function resolveResumeSession(
  sessionId: string | undefined,
  hasTranscript: (id: string) => boolean,
): string | undefined {
  if (!sessionId) return sessionId;
  if (!UUID_RE.test(sessionId)) {
    log(`session id ${sessionId} is not a resumable UUID (arizuko placeholder) — starting fresh`);
    return undefined;
  }
  if (!hasTranscript(sessionId)) {
    log(`session id ${sessionId} has no transcript on disk (pruned or never persisted) — starting fresh`);
    return undefined;
  }
  return sessionId;
}

// isRateLimitRejected reports whether an SDKRateLimitEvent means the turn is
// actually throttled (status 'rejected'), vs an informational usage update.
// rate_limit was the top failure signal (195/2d) and most of that volume is
// informational; only 'rejected' means the user's turn went silent.
export function isRateLimitRejected(raw: Record<string, unknown>): boolean {
  const info = raw.rate_limit_info as { status?: string } | undefined;
  return info?.status === 'rejected';
}

const MAX_QUEUE = 100;
// Standard-context Opus is the platform default — subscription-covered, never
// the 1M-context beta, which needs usage credits the subscription excludes.
// Leaving model undefined lets the SDK pick a 1M-context model → "Usage credits
// required for 1M context" on every turn.
const DEFAULT_MODEL = 'claude-opus-5';

interface SDKUserMessage {
  type: 'user';
  message: { role: 'user'; content: string };
  parent_tool_use_id: null;
  session_id: string;
}

function log(message: string): void {
  console.error(`[ant] ${message}`);
}

// MessageStream is the async-iterable prompt source the SDK consumes. Steering
// pushes new user turns; end() closes the turn.
class MessageStream {
  private queue: SDKUserMessage[] = [];
  private waiting: (() => void) | null = null;
  private done = false;

  push(text: string): void {
    if (this.queue.length >= MAX_QUEUE) {
      log(`MessageStream queue full (${this.queue.length}); dropping message`);
      return;
    }
    this.queue.push({
      type: 'user',
      message: { role: 'user', content: text },
      parent_tool_use_id: null,
      session_id: '',
    });
    this.waiting?.();
  }

  end(): void {
    this.done = true;
    this.waiting?.();
  }

  async *[Symbol.asyncIterator](): AsyncGenerator<SDKUserMessage> {
    while (true) {
      while (this.queue.length > 0) {
        yield this.queue.shift()!;
      }
      if (this.done) return;
      await new Promise<void>(r => { this.waiting = r; });
      this.waiting = null;
    }
  }
}

interface ParsedMessage {
  role: 'user' | 'assistant';
  content: string;
}

function parseTranscript(content: string): ParsedMessage[] {
  const messages: ParsedMessage[] = [];
  for (const line of content.split('\n')) {
    if (!line.trim()) continue;
    try {
      const entry = JSON.parse(line);
      const c = entry.message?.content;
      if (!c) continue;
      if (entry.type === 'user') {
        const text = typeof c === 'string' ? c : c.map((p: { text?: string }) => p.text || '').join('');
        if (text) messages.push({ role: 'user', content: text });
      } else if (entry.type === 'assistant') {
        const text = c.filter((p: { type: string }) => p.type === 'text').map((p: { text: string }) => p.text).join('');
        if (text) messages.push({ role: 'assistant', content: text });
      }
    } catch { /* skip malformed line */ }
  }
  return messages;
}

function formatTranscriptMarkdown(messages: ParsedMessage[], title?: string | null, assistantName?: string): string {
  const when = new Date().toLocaleString('en-US', {
    month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', hour12: true,
  });
  const body = messages.map(m => {
    const sender = m.role === 'user' ? 'User' : (assistantName || 'Assistant');
    const content = m.content.length > 2000 ? m.content.slice(0, 2000) + '...' : m.content;
    return `**${sender}**: ${content}\n`;
  }).join('\n');
  return `# ${title || 'Conversation'}\n\nArchived: ${when}\n\n---\n\n${body}`;
}

interface SessionEntry {
  sessionId: string;
  summary: string;
}

function getSessionSummary(sessionId: string, transcriptPath: string): string | null {
  const indexPath = path.join(path.dirname(transcriptPath), 'sessions-index.json');
  try {
    const index = JSON.parse(fs.readFileSync(indexPath, 'utf-8')) as { entries: SessionEntry[] };
    return index.entries.find(e => e.sessionId === sessionId)?.summary ?? null;
  } catch {
    return null;
  }
}

// assistantNameFromSettings reads the name the PreCompact archival hook signs
// transcripts with. seedSettings writes it into the settings env block, which is
// NOT real container env, so process.env never carries it.
function assistantNameFromSettings(): string | undefined {
  try {
    const s = JSON.parse(fs.readFileSync(`${HOME}/.claude/settings.json`, 'utf-8'));
    const name = s?.env?.ARIZUKO_ASSISTANT_NAME;
    return typeof name === 'string' && name !== '' ? name : undefined;
  } catch {
    return undefined;
  }
}

// claudeEnv is claude's translation of the gateway pair (gateway.ts): the SDK
// subprocess reads ANTHROPIC_BASE_URL and sends ANTHROPIC_AUTH_TOKEN as its
// bearer. Measured 2026-09-04 against a local sink: with no real credential
// set and ANTHROPIC_AUTH_TOKEN=virtual-key-abc123, the request carried
// exactly `Authorization: Bearer virtual-key-abc123`. No gateway returns the
// same object, so the SDK sees the env it sees today.
export function claudeEnv(env: Record<string, string>): Record<string, string> {
  const gw = modelGateway(env);
  if (!gw) return env;
  const out: Record<string, string> = { ...env, ANTHROPIC_BASE_URL: gw.url };
  if (gw.key) out.ANTHROPIC_AUTH_TOKEN = gw.key;
  return out;
}

// extraDirs lists the additional context directories for one turn: the shared
// read-only tree (skipped on an elevated /root turn) and every operator mount
// under /mnt.
function extraDirs(env: Record<string, string>): string[] {
  const dirs: string[] = [];
  // 4/R: root is an operator /root elevation, NOT a folder shape — a top-level
  // world has no slash but is NOT root. Read the marker, never derive it.
  const isRoot = env.ARIZUKO_IS_ROOT === '1';
  if (!isRoot && fs.existsSync('/var/lib/share')) dirs.push('/var/lib/share');
  try {
    for (const e of fs.readdirSync('/mnt')) {
      const p = path.join('/mnt', e);
      if (fs.statSync(p).isDirectory()) dirs.push(p);
    }
  } catch { /* /mnt absent */ }
  return dirs;
}

function createPreCompactHook(assistantName?: string): HookCallback {
  return async (input, _toolUseId, _context) => {
    const { transcript_path: transcriptPath, session_id: sessionId } = input as PreCompactHookInput;
    try {
      const messages = parseTranscript(fs.readFileSync(transcriptPath, 'utf-8'));
      if (messages.length === 0) {
        log('No messages to archive');
      } else {
        const summary = getSessionSummary(sessionId, transcriptPath);
        const slug = summary
          ? summary.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 50)
          : `conversation-${new Date().toTimeString().slice(0, 5).replace(':', '')}`;
        const dir = `${HOME}/conversations`;
        fs.mkdirSync(dir, { recursive: true });
        const fp = path.join(dir, `${new Date().toISOString().split('T')[0]}-${slug}.md`);
        fs.writeFileSync(fp, formatTranscriptMarkdown(messages, summary, assistantName));
        log(`Archived conversation to ${fp}`);
      }
    } catch (err) {
      log(`Failed to archive transcript: ${err instanceof Error ? err.message : String(err)}`);
    }

    return {
      systemMessage:
        'Context is about to be compacted. Invoke /diary before continuing.\n\n' +
        'Preserve references to these in the summary:\n' +
        '- PERSONA.md (your identity and persona)\n' +
        '- CLAUDE.md (project instructions)\n' +
        '- diary/ entries (recent decisions and progress)\n' +
        '- facts/ (researched knowledge)\n' +
        '- users/ (user profiles and preferences)\n' +
        '- Any open tasks, pending work, or unresolved questions',
    };
  };
}

// claudeHooks is the claude RENDERER (spec 6/7): it turns policy.ts's binding
// table into the SDK's hooks object. claude honours all four verdicts, so no row
// is refused here.
function claudeHooks(rows: Binding[]): Record<PolicyEvent, Array<{ matcher?: string; hooks: HookCallback[] }>> {
  const forEvent = (event: PolicyEvent) =>
    rows
      .filter((b) => b.event === event)
      .map((b) => ({ ...(b.matcher ? { matcher: b.matcher } : {}), hooks: [asHookCallback(b)] }));
  return { PreToolUse: forEvent('PreToolUse'), PostToolUse: forEvent('PostToolUse') };
}

function asHookCallback(b: Binding): HookCallback {
  return async (input, _toolUseId, _context) => {
    const verdict = await runRow(b, input as unknown as ToolEvent);
    switch (verdict.kind) {
      case 'allow':
        return {};
      case 'deny':
        return {
          hookSpecificOutput: {
            hookEventName: 'PreToolUse',
            permissionDecision: 'deny',
            permissionDecisionReason: verdict.reason,
          },
        };
      case 'rewrite':
        return {
          hookSpecificOutput: { hookEventName: 'PreToolUse', updatedInput: verdict.input },
        };
      case 'inject':
        log(`policy ${b.id}: injecting context after a tool call`);
        return {
          hookSpecificOutput: { hookEventName: 'PostToolUse', additionalContext: verdict.context },
        };
    }
  };
}

// extractModelUsage converts the SDK's modelUsage record to the snake_case
// shape gated expects. costUSD → cost_cents via × 100 + round. Spec 11/19.
function extractModelUsage(modelUsage: unknown): Record<string, ModelUsage> | undefined {
  if (!modelUsage || typeof modelUsage !== 'object') return undefined;
  const out: Record<string, ModelUsage> = {};
  for (const [model, u] of Object.entries(modelUsage as Record<string, {
    inputTokens?: number;
    outputTokens?: number;
    cacheReadInputTokens?: number;
    cacheCreationInputTokens?: number;
    costUSD?: number;
  }>)) {
    out[model] = {
      input: u.inputTokens ?? 0,
      output: u.outputTokens ?? 0,
      cache_read: u.cacheReadInputTokens ?? 0,
      cache_write: u.cacheCreationInputTokens ?? 0,
      cost_cents: Math.round((u.costUSD ?? 0) * 100),
    };
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

export function claudeResultStatus(
  subtype: string | undefined,
  result: string | undefined,
): 'success' | 'error' {
  if (subtype !== 'success') return 'error';
  return result?.trim() === 'Not logged in · Please run /login' ? 'error' : 'success';
}

// assistantText flattens an SDK assistant message's text parts. This is claude's
// raw shape — reading it anywhere outside this file was the <status> bug.
function assistantText(raw: Record<string, unknown>): string {
  const content = (raw as { message?: { content?: unknown } }).message?.content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((p: { type?: string }) => p.type === 'text')
    .map((p: { text?: string }) => p.text || '')
    .join('');
}

// RATE_LIMIT_NOTICE rides the one interim-status pipeline.
const RATE_LIMIT_NOTICE = "<status>⏳ Hit a rate limit — I'll continue shortly.</status>";

// ClaudeTurn reduces the SDK message stream to frames. Pure — the surrounding
// path runs inside query(), which needs real credentials (BUGS F6.3).
export class ClaudeTurn {
  // sessionId is captured from system/init so continuity frames and the
  // max-turns summary subquery can name the session.
  sessionId: string | undefined;

  push(message: unknown): Frame[] {
    const m = message as { type?: string; subtype?: string; session_id?: string };
    const raw = message as Record<string, unknown>;

    if (m.type === 'system' && m.subtype === 'init') {
      this.sessionId = m.session_id;
      if (!this.sessionId) return [];
      return [{ kind: 'continuity', token: this.sessionId, sessionId: this.sessionId }];
    }
    if (m.type === 'assistant') {
      const frames: Frame[] = [];
      // Each assistant message advances the resume point: the packed token lets the
      // next inner-loop turn resume AT this message instead of after trailing tool
      // noise.
      const uuid = (raw as { uuid?: string }).uuid;
      if (uuid && this.sessionId) {
        frames.push({
          kind: 'continuity',
          token: packContinuity(this.sessionId, uuid),
          sessionId: this.sessionId,
        });
      }
      const text = assistantText(raw);
      if (text) frames.push({ kind: 'status', text });
      return frames;
    }
    if (m.type === 'rate_limit_event') {
      return isRateLimitRejected(raw) ? [{ kind: 'status', text: RATE_LIMIT_NOTICE }] : [];
    }
    if (m.type === 'result') {
      const text = 'result' in raw ? (raw as { result?: string }).result ?? undefined : undefined;
      const status = claudeResultStatus(m.subtype, text);
      return [{
        kind: 'result',
        ok: status === 'success',
        text,
        usage: extractModelUsage((raw as { modelUsage?: unknown }).modelUsage),
        // A resume miss ("No conversation found") surfaces as this subtype; it is a
        // retry signal, never a user-facing reply.
        retryFresh: m.subtype === 'error_during_execution' || undefined,
      }];
    }
    return [];
  }
}

async function summarizeMaxTurns(resumeId: string | undefined): Promise<ResultFrame | null> {
  if (!resumeId) return null;
  log('Max turns hit; requesting summary + resumption nudge');
  let text = 'ran out of turns; say "continue" to resume.';
  for await (const msg of query({
    prompt: 'You ran out of turns mid-task. Summarise concisely: what you accomplished, what is still pending. Then tell the user they can say "continue" to resume where you left off.',
    options: {
      cwd: HOME,
      maxTurns: 3,
      resume: resumeId,
      permissionMode: 'bypassPermissions' as const,
      allowDangerouslySkipPermissions: true,
      sandbox: { enabled: false },
    },
  })) {
    if (msg.type === 'result') {
      text = (msg as { result?: string }).result ?? text;
    }
  }
  return { kind: 'result', ok: true, text };
}

// runClaude is one turn: unpack the continuity token, drive query(), reduce
// the SDK stream to frames.
async function* runClaude(turn: Turn): AsyncGenerator<Frame> {
  const { sessionId, resumeAt } = unpackContinuity(turn.continuity);
  // A lineage placeholder or a pruned transcript starts fresh silently.
  const resume = resolveResumeSession(sessionId, (id) => fs.existsSync(transcriptPath(id)));

  const stream = new MessageStream();
  stream.push(turn.prompt);
  const abortController = new AbortController();
  // Set only when the abort came from the query-timeout deadline, so the aborted
  // for-await delivers a graceful summary instead of a code-1 silent exit.
  let timedOut = false;
  const timer = setTimeout(() => {
    log(`Query timeout (${QUERY_TIMEOUT_MS}ms) reached, aborting`);
    timedOut = true;
    abortController.abort();
    stream.end();
  }, QUERY_TIMEOUT_MS);
  // The platform winding down (close sentinel) ends the prompt stream so the SDK
  // concludes gracefully.
  const onAbort = () => stream.end();
  turn.signal?.addEventListener('abort', onAbort, { once: true });

  const reducer = new ClaudeTurn();
  try {
    for await (const message of query({
      prompt: stream,
      options: {
        abortController,
        cwd: turn.cwd,
        additionalDirectories: (() => { const d = extraDirs(turn.env); return d.length > 0 ? d : undefined; })(),
        resume,
        // resumeAt travels only inside the packed token, so a fresh start never carries
        // a stale resume point.
        resumeSessionAt: resume ? resumeAt : undefined,
        // The chassis renders the text layers; absent means claude's own preset.
        systemPrompt: turn.systemPrompt ?? { type: 'preset' as const, preset: 'claude_code' as const },
        model: turn.model || DEFAULT_MODEL,
        // Reasoning depth. Absent means the CLI's own default (high), so an
        // instance that sets nothing behaves exactly as before.
        effort: turn.effort as EffortLevel | undefined,
        // No allowedTools allowlist: under bypassPermissions every tool is available
        // with no prompt (allowedTools is only an auto-approve hint, not a restriction).
        // arizuko gates side effects at the gated MCP socket and the egress proxy.
        // sandbox off for the same reason — the container IS the sandbox.
        sandbox: { enabled: false },
        env: claudeEnv(turn.env),
        permissionMode: 'bypassPermissions',
        allowDangerouslySkipPermissions: true,
        settingSources: ['project', 'user'],
        // strictMcpConfig: mcpServers below is the ONLY server map. Without it the SDK
        // also reads `.mcp.json` from the query cwd — which for a turn IS /home/node, a
        // directory the agent writes. That server would be dialled straight from the
        // SDK, never through ipc.serveConn, so no hold:mcp:<tool> rule could suspend it
        // and no audit row would exist (BUGS J14).
        strictMcpConfig: true,
        mcpServers: turn.tools ?? {},
        // PreCompact is a turn-boundary hook, not a tool policy. Everything tool-scoped
        // comes from policy.ts's one table.
        hooks: {
          PreCompact: [{ hooks: [createPreCompactHook(assistantNameFromSettings())] }],
          ...claudeHooks(bindings({ home: os.homedir(), turnID: turn.turnID })),
        },
      },
    })) {
      const m = message as { type?: string; subtype?: string };
      if (m.type === 'system' && m.subtype === 'task_notification') {
        const tn = message as { task_id: string; status: string; summary: string };
        log(`Task notification: task=${tn.task_id} status=${tn.status} summary=${tn.summary}`);
      }
      // A result terminates the turn: close the prompt stream so the SDK concludes.
      if (m.type === 'result') stream.end();
      // error_max_turns is claude-specific: the harness ran out of turns mid-task. Run
      // a summary subquery and emit one success result carrying it.
      if (m.type === 'result' && m.subtype === 'error_max_turns') {
        const summary = await summarizeMaxTurns(reducer.sessionId);
        // No session to resume → deliver nothing (pre-seam behavior).
        if (summary) yield summary;
        continue;
      }
      yield* reducer.push(message);
    }
  } catch (err) {
    // The query-timeout abort surfaces here as a thrown AbortError. Without this
    // catch it propagated out of the runtime → container exit 1 → the user got NO
    // reply. A manual interrupt (no timedOut flag) re-throws; runed's hard kill
    // bounds it.
    if (!timedOut) throw err;
    log('Query timeout: delivering graceful summary instead of aborting silently');
    const summary = await summarizeMaxTurns(reducer.sessionId);
    // No resumable session for a summary subquery → the shared minimal graceful
    // turn; the user still gets one reply, never silence.
    yield summary ? { ...summary, timedOut: true } : timeoutFrame();
  } finally {
    clearTimeout(timer);
    turn.signal?.removeEventListener('abort', onAbort);
    stream.end();
  }
}

// claudeHarness is the default occupant (spec 6/7 § "The harness seam").
export const claudeHarness: Harness = {
  name: 'claude',
  profile: {
    continuity: 'native',
    tools: 'mcp',
    guard: 'write-time',
    steering: 'mid-turn',
    // native: settingSources ['project','user'] makes Claude Code load
    // ~/.claude/CLAUDE.md itself; rendering it too would ship it twice.
    manual: 'native',
    status: 'streaming',
  },
  run: runClaude,
};
