// Drives Claude Code via @anthropic-ai/claude-agent-sdk query() — the claude
// backend, and the default occupant. ant never implements an agent loop
// itself: model calls, tool execution, retries and session state all live in
// the harness; this module only spawns it, normalizes its event stream, and
// hands the turn back to index.ts, which reports it once over submit_turn.
// The seam it implements is backend.ts (spec 6/7 § "The connector seam"); the
// canonical replaceable seam stays the MCP socket at the process boundary
// (spec 5/P § "The container model").

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
import type { Backend, DrainIpcInput, Event, SessionConfig } from './backend.js';

const HOME = '/home/node';

// A resumable Claude Code session id is a UUID. arizuko's lineage placeholders
// (sess-<nano>, fork hex) are not, and `claude --resume` rejects them — so they
// must be treated as "no session yet" (start fresh), not passed to --resume.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

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

// isSessionError reports whether an SDK result subtype means the resume target
// was unusable ("No conversation found"). Such a result is a retry signal, NEVER
// a user-facing reply — deliverTurn is skipped for it (a resume miss is not a
// user error). BUGS: stale chat-bound session.
export function isSessionError(subtype: string | undefined): boolean {
  return subtype === 'error_during_execution';
}
const MAX_QUEUE = 100;
const QUERY_TIMEOUT_MS = Number(process.env.ARIZUKO_QUERY_TIMEOUT_MS) || 15 * 60_000;
// Standard-context Opus is the platform default — subscription-covered, never
// the 1M-context beta (which needs usage credits the subscription excludes).
// Leaving model undefined lets the SDK pick its own default, which now resolves
// to a 1M-context model → "Usage credits required for 1M context" on every turn.
// A group may still override via cfg.model.
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
// active query. The claude backend owns the hook wiring (it's
// claude-SDK-specific); the runtime owns the IPC mechanism (DrainIpcInput).
function createIpcDrainHook(drain: DrainIpcInput): HookCallback {
  return async (_input, _toolUseId, _context) => {
    const messages = drain();
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

// ClaudeSession wraps one query() call and normalizes its NDJSON-style SDK
// messages into an Event stream. raw preserves the full SDK message.
export class ClaudeSession {
  private stream = new MessageStream();
  private abortController = new AbortController();
  private timeoutTimer: ReturnType<typeof setTimeout>;
  // Set only when the abort came from the query-timeout deadline (not a manual
  // interrupt() or close()). events() reads it to deliver a graceful summary
  // instead of letting the aborted for-await throw into a code-1 silent exit.
  private timedOut = false;

  constructor(private cfg: SessionConfig, private drain: DrainIpcInput) {
    this.stream.push(cfg.prompt);
    this.timeoutTimer = setTimeout(() => {
      log(`Query timeout (${QUERY_TIMEOUT_MS}ms) reached, aborting`);
      this.timedOut = true;
      this.abortController.abort();
      this.stream.end();
    }, QUERY_TIMEOUT_MS);
  }

  async close(): Promise<void> {
    clearTimeout(this.timeoutTimer);
    this.stream.end();
  }

  async *events(): AsyncIterable<Event> {
    const cfg = this.cfg;
    const extraDirs = cfg.addDirs ?? [];
    const agentMcpServers = cfg.mcpServers ?? {};
    const groupModel = cfg.model || DEFAULT_MODEL;

    try {
      for await (const message of query({
        prompt: this.stream,
        options: {
          abortController: this.abortController,
          cwd: cfg.cwd ?? HOME,
          additionalDirectories: extraDirs.length > 0 ? extraDirs : undefined,
          resume: cfg.resume,
          resumeSessionAt: cfg.resumeAt,
          systemPrompt: cfg.systemPrompt,
          model: groupModel,
          // No allowedTools allowlist: under bypassPermissions every tool is
          // available with no prompt (allowedTools is only an auto-approve hint,
          // not a restriction). arizuko gates side effects at the gated MCP
          // socket + the egress proxy, never Claude Code's permission layer.
          // sandbox off for the same reason — the container IS the sandbox.
          sandbox: { enabled: false },
          env: cfg.env,
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
          mcpServers: agentMcpServers,
          hooks: {
            PreCompact: [{ hooks: [createPreCompactHook(cfg.assistantName)] }],
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
              { hooks: [createIpcDrainHook(this.drain), createToolLogPostHook()] },
              // TodoWrite → one live-edited ⏳ checklist per turn (spec 5/24).
              { matcher: 'TodoWrite', hooks: [createTodoStatusHook(cfg.turnID ?? '')] },
            ],
          },
        },
      })) {
        const m = message as { type?: string; subtype?: string };
        if (m.type === 'system' && m.subtype === 'task_notification') {
          const tn = message as { task_id: string; status: string; summary: string };
          log(`Task notification: task=${tn.task_id} status=${tn.status} summary=${tn.summary}`);
        }
        const ev = normalize(message);
        if (!ev) continue;
        if (ev.type === 'system_init' && ev.sessionId) this.sessionId = ev.sessionId;
        // A result terminates the turn: close the prompt stream so the SDK
        // generator concludes (matches the pre-seam stream.end() on result).
        if (ev.type === 'result') this.stream.end();
        // error_max_turns is a claude-specific terminal state: the harness ran
        // out of turns mid-task. Run a short summary subquery and emit a success
        // result carrying the summary, so the runtime delivers exactly one turn
        // (behavior preserved from the pre-seam runtime).
        if (ev.type === 'result' && (ev.raw as { subtype?: string }).subtype === 'error_max_turns') {
          const summary = await this.summarizeMaxTurns(this.sessionId);
          // No session to resume → deliver nothing (pre-seam behavior).
          if (summary) yield summary;
          continue;
        }
        yield ev;
      }
    } catch (err) {
      // The query-timeout abort surfaces here as a thrown AbortError. Without
      // this catch it propagated out of the runtime → container exit code 1 →
      // the user got NO reply (silent failure on a long turn). Deliver exactly
      // one graceful result instead. A manual interrupt() (no timedOut flag)
      // re-throws to preserve today's behavior; runed's hard kill bounds it.
      if (!this.timedOut) throw err;
      log('Query timeout: delivering graceful summary instead of aborting silently');
      const summary = (await this.summarizeMaxTurns(this.sessionId)) ?? this.timeoutFallback();
      yield { ...summary, timedOut: true };
    } finally {
      clearTimeout(this.timeoutTimer);
    }
  }

  // timeoutFallback is the minimal graceful turn when no session is resumable
  // for a summary subquery — the user still gets one reply, never silence.
  private timeoutFallback(): Event {
    return {
      type: 'result',
      raw: { type: 'result', subtype: 'success' },
      text: 'Hit the time limit on this turn — narrow the request or ask me to continue.',
      final: true,
      status: 'success',
    };
  }

  // sessionId is captured from the system/init event so the max-turns summary
  // subquery can resume the same session.
  private sessionId: string | undefined;

  private async summarizeMaxTurns(resumeId: string | undefined): Promise<Event | null> {
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
    return { type: 'result', raw: { type: 'result', subtype: 'success' }, text, final: true, status: 'success' };
  }
}

export function claudeResultStatus(
  subtype: string | undefined,
  result: string | undefined,
): 'success' | 'error' {
  if (subtype !== 'success') return 'error';
  return result?.trim() === 'Not logged in · Please run /login' ? 'error' : 'success';
}

// normalize maps one SDK message onto an Event. Messages that carry no
// normalized category (partial deltas, internal status) are dropped — the
// runtime never needed them. raw always preserves the full SDK message.
//
// Exported for claude.test.ts: this mapping is the load-bearing part, and a
// direct test is the only way to cover it — the surrounding path runs inside
// the SDK's query(), which needs real credentials.
export function normalize(message: unknown): Event | null {
  const m = message as { type?: string; subtype?: string; session_id?: string };
  const raw = message as Record<string, unknown>;

  if (m.type === 'system' && m.subtype === 'init') {
    return { type: 'system_init', raw, sessionId: m.session_id };
  }
  if (m.type === 'assistant') {
    return { type: 'assistant', raw };
  }
  if (m.type === 'user') {
    return { type: 'tool_result', raw };
  }
  if (m.type === 'rate_limit_event') {
    return { type: 'rate_limit', raw };
  }
  if (m.type === 'result') {
    const textResult = 'result' in raw ? (raw as { result?: string }).result ?? undefined : undefined;
    const models = extractModelUsage((raw as { modelUsage?: unknown }).modelUsage);
    const status = claudeResultStatus(m.subtype, textResult);
    return { type: 'result', raw, text: textResult, final: true, status, models };
  }
  return null;
}

// claudeBackend is the default occupant (spec 6/7 § "The connector seam").
export const claudeBackend: Backend = {
  name: 'claude',
  resolveResume: (sessionId) =>
    resolveResumeSession(sessionId, (id) => fs.existsSync(transcriptPath(id))),
  isSessionError,
  spawn: (cfg, drain) => new ClaudeSession(cfg, drain),
};
