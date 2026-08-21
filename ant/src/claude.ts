// Drives Claude Code via @anthropic-ai/claude-agent-sdk query() — the claude
// harness, and the default occupant. ant never implements an agent loop
// itself: model calls, tool execution, retries and session state all live in
// the harness; this module only spawns it, normalizes its message stream into
// frames, and hands the turn back to index.ts, which reports it once over
// submit_turn. The seam it implements is backend.ts (spec 6/7 § "The harness
// seam"); the canonical replaceable seam stays the MCP socket at the process
// boundary (spec 5/P § "The container model").

import fs from 'fs';
import os from 'os';
import path from 'path';
import {
  query,
  HookCallback,
  PreCompactHookInput,
} from '@anthropic-ai/claude-agent-sdk';
import { createToolLogPreHook, createToolLogPostHook } from './tool-log.js';
import { createTodoStatusHook } from './todo-status.js';
import { guardPreToolUse, guardPostToolUse } from './guards.js';
import type { ModelUsage } from './mcp.js';
import { QUERY_TIMEOUT_MS, timeoutFrame } from './backend.js';
import type { Frame, Harness, ResultFrame, Steer, Turn } from './backend.js';

const HOME = '/home/node';

// A resumable Claude Code session id is a UUID. arizuko's lineage placeholders
// (sess-<nano>, fork hex) are not, and `claude --resume` rejects them — so they
// must be treated as "no session yet" (start fresh), not passed to --resume.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// The continuity token is claude's own pack: a bare session UUID (what the
// platform hands back across spawns — routd's lineage row stores the plain
// sessionId frames carry), or sessionId#resumeAt intra-run so the next
// inner-loop turn resumes at the last assistant message. '#' never appears in
// a UUID, so the pack is unambiguous. The chassis never parses this.
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
// returning undefined ("start fresh") when the id is not resumable:
//  - an id that is not a UUID is an arizuko lineage placeholder
//    (sess-<nano> / fork hex) — `claude --resume` rejects it;
//  - a UUID whose transcript is absent on disk (never persisted, or pruned by
//    Claude Code retention) — resuming it returns "No conversation found" →
//    error_during_execution + silent context loss (BUGS: stale chat-bound
//    session). Start fresh silently instead of burning a turn on the resume miss.
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
// actually throttled (status 'rejected'), vs. an 'allowed'/'allowed_warning'
// informational usage update, which fires far more often and needs no
// user-facing notice. BUGS: rate_limit was the top failure signal (195/2d) —
// most of that volume is informational; only 'rejected' means the user's turn
// actually went silent.
export function isRateLimitRejected(raw: Record<string, unknown>): boolean {
  const info = raw.rate_limit_info as { status?: string } | undefined;
  return info?.status === 'rejected';
}

const MAX_QUEUE = 100;
// Standard-context Opus is the platform default — subscription-covered, never
// the 1M-context beta (which needs usage credits the subscription excludes).
// Leaving model undefined lets the SDK pick its own default, which now resolves
// to a 1M-context model → "Usage credits required for 1M context" on every turn.
// A group may still override via turn.model.
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
// transcripts with. seedSettings writes ARIZUKO_ASSISTANT_NAME into the
// settings env block (container/runner.go) — the same file ant already reads
// for outputStyle; it is NOT real container env (only -e vars are), so
// process.env never carries it. Claude wiring, so it lives here, not the seam.
function assistantNameFromSettings(): string | undefined {
  try {
    const s = JSON.parse(fs.readFileSync(`${HOME}/.claude/settings.json`, 'utf-8'));
    const name = s?.env?.ARIZUKO_ASSISTANT_NAME;
    return typeof name === 'string' && name !== '' ? name : undefined;
  } catch {
    return undefined;
  }
}

// extraDirs lists the additional context directories for one turn: the shared
// read-only tree (skipped on an elevated /root turn, which mounts
// /var/lib/groups instead) and every operator mount under /mnt. Claude wiring
// — the SDK's additionalDirectories — so it lives here, not the seam.
function extraDirs(env: Record<string, string>): string[] {
  const dirs: string[] = [];
  // 4/R: root is an operator /root elevation, NOT a folder shape — a top-level
  // world has no slash but is NOT root. Read the elevation marker the gateway
  // sets, never derive it from the path.
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

// The IPC-steering PostToolUse hook pulls mid-turn user messages into the
// active query. The claude harness owns the hook wiring (it's
// claude-SDK-specific); the chassis owns the IPC mechanism (Steer).
function createIpcDrainHook(steer: Steer): HookCallback {
  return async (_input, _toolUseId, _context) => {
    const messages = steer();
    if (messages.length === 0) return {};
    log(`Piping ${messages.length} IPC messages into active query via PostToolUse hook`);
    return {
      hookSpecificOutput: {
        hookEventName: 'PostToolUse',
        additionalContext: `<user-steering>\n${messages.join('\n')}\n</user-steering>`,
      },
    };
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

// assistantText flattens an SDK assistant message's text parts, mirroring
// parseTranscript's assistant branch. This is claude's raw shape
// (message.content[]) — reading it anywhere outside this file was the
// <status> bug: codex's raw has no such nesting.
function assistantText(raw: Record<string, unknown>): string {
  const content = (raw as { message?: { content?: unknown } }).message?.content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((p: { type?: string }) => p.type === 'text')
    .map((p: { text?: string }) => p.text || '')
    .join('');
}

// RATE_LIMIT_NOTICE is emitted as a <status> block so it rides the one
// interim-status pipeline (chassis extracts, dedups once per turn, submits).
const RATE_LIMIT_NOTICE = "<status>⏳ Hit a rate limit — I'll continue shortly.</status>";

// ClaudeTurn reduces the SDK message stream to frames. Pure — the load-bearing
// mapping, directly testable; the surrounding path runs inside the SDK's
// query(), which needs real credentials (BUGS F6.3).
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
      // Each assistant message advances the resume point: the packed token
      // lets the next inner-loop turn resume AT this message (resumeSessionAt)
      // instead of after trailing tool noise.
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
        // A resume miss ("No conversation found") surfaces as this subtype; it
        // is a retry signal, never a user-facing reply (BUGS: stale
        // chat-bound session).
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
async function* runClaude(turn: Turn, steer: Steer): AsyncGenerator<Frame> {
  const { sessionId, resumeAt } = unpackContinuity(turn.continuity);
  // A lineage placeholder or a pruned transcript starts fresh silently.
  const resume = resolveResumeSession(sessionId, (id) => fs.existsSync(transcriptPath(id)));

  const stream = new MessageStream();
  stream.push(turn.prompt);
  const abortController = new AbortController();
  // Set only when the abort came from the query-timeout deadline. Read below
  // to deliver a graceful summary instead of letting the aborted for-await
  // throw into a code-1 silent exit.
  let timedOut = false;
  const timer = setTimeout(() => {
    log(`Query timeout (${QUERY_TIMEOUT_MS}ms) reached, aborting`);
    timedOut = true;
    abortController.abort();
    stream.end();
  }, QUERY_TIMEOUT_MS);
  // The platform winding down (close sentinel) ends the prompt stream so the
  // SDK concludes gracefully — the pre-seam Session.close() behavior.
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
        // resumeAt travels only inside the packed token, so a fresh start
        // (resume undefined) never carries a stale resume point.
        resumeSessionAt: resume ? resumeAt : undefined,
        // The chassis renders the text layers; absent means claude's own
        // preset — the SDK union is claude wiring, kept out of the seam.
        systemPrompt: turn.systemPrompt ?? { type: 'preset' as const, preset: 'claude_code' as const },
        model: turn.model || DEFAULT_MODEL,
        // No allowedTools allowlist: under bypassPermissions every tool is
        // available with no prompt (allowedTools is only an auto-approve hint,
        // not a restriction). arizuko gates side effects at the gated MCP
        // socket + the egress proxy, never Claude Code's permission layer.
        // sandbox off for the same reason — the container IS the sandbox.
        sandbox: { enabled: false },
        env: turn.env,
        permissionMode: 'bypassPermissions',
        allowDangerouslySkipPermissions: true,
        settingSources: ['project', 'user'],
        // strictMcpConfig: mcpServers below is the ONLY server map. Without
        // it the SDK also reads `.mcp.json` from the query cwd — which for a
        // turn IS /home/node, a directory the agent writes. That server would
        // be dialled straight from the SDK, never through ipc.serveConn, so
        // no hold:mcp:<tool> rule could suspend it and no audit row would
        // exist. It is the same self-grant J1 closed on settings.json, via a
        // second door (BUGS J14). Third-party MCP arrives through plugins and
        // connectors, both of which route through the arizuko socket.
        strictMcpConfig: true,
        mcpServers: turn.tools ?? {},
        hooks: {
          PreCompact: [{ hooks: [createPreCompactHook(assistantNameFromSettings())] }],
          // Guards first, then observers. guards.ts is the ONLY place a
          // refusal is registered — read guardPreToolUse/guardPostToolUse and
          // you have the agent's whole safety surface. Everything below them
          // observes and never denies.
          PreToolUse: [
            ...guardPreToolUse(),
            { hooks: [createToolLogPreHook()] },
          ],
          PostToolUse: [
            ...guardPostToolUse(os.homedir()),
            { hooks: [createIpcDrainHook(steer), createToolLogPostHook()] },
            // TodoWrite → one live-edited ⏳ checklist per turn (spec 5/24).
            { matcher: 'TodoWrite', hooks: [createTodoStatusHook(turn.turnID)] },
          ],
        },
      },
    })) {
      const m = message as { type?: string; subtype?: string };
      if (m.type === 'system' && m.subtype === 'task_notification') {
        const tn = message as { task_id: string; status: string; summary: string };
        log(`Task notification: task=${tn.task_id} status=${tn.status} summary=${tn.summary}`);
      }
      // A result terminates the turn: close the prompt stream so the SDK
      // generator concludes.
      if (m.type === 'result') stream.end();
      // error_max_turns is a claude-specific terminal state: the harness ran
      // out of turns mid-task. Run a short summary subquery and emit a success
      // result carrying the summary, so the chassis delivers exactly one turn.
      if (m.type === 'result' && m.subtype === 'error_max_turns') {
        const summary = await summarizeMaxTurns(reducer.sessionId);
        // No session to resume → deliver nothing (pre-seam behavior).
        if (summary) yield summary;
        continue;
      }
      yield* reducer.push(message);
    }
  } catch (err) {
    // The query-timeout abort surfaces here as a thrown AbortError. Without
    // this catch it propagated out of the runtime → container exit code 1 →
    // the user got NO reply (silent failure on a long turn). Deliver exactly
    // one graceful result instead. A manual interrupt (no timedOut flag)
    // re-throws to preserve the pre-seam behavior; runed's hard kill bounds it.
    if (!timedOut) throw err;
    log('Query timeout: delivering graceful summary instead of aborting silently');
    const summary = await summarizeMaxTurns(reducer.sessionId);
    // No resumable session for a summary subquery → the shared minimal
    // graceful turn; the user still gets one reply, never silence.
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
    status: 'streaming',
  },
  run: runClaude,
};
