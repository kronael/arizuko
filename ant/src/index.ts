import fs from 'fs';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { submitTurn, submitStatus } from './mcp.js';
import { loadAgentMcpServers } from './mcp-servers.js';
import { Backend, Session, SessionConfig, selectBackend, renderMcpServers } from './backend/index.js';

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
//  - a claude id that is not a UUID is an arizuko lineage placeholder
//    (sess-<nano> / fork hex) — `claude --resume` rejects it;
//  - a UUID whose transcript is absent on disk (never persisted, or pruned by
//    Claude Code retention) — resuming it returns "No conversation found" →
//    error_during_execution + silent context loss (BUGS: stale chat-bound
//    session). Start fresh silently instead of burning a turn on the resume miss.
// Non-claude backends carry their own id format — passed through untouched.
export function resolveResumeSession(
  backendName: string,
  sessionId: string | undefined,
  hasTranscript: (id: string) => boolean,
): string | undefined {
  if (backendName !== 'claude' || !sessionId) return sessionId;
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

interface ContainerInput {
  prompt: string;
  sessionId?: string;
  groupFolder: string;
  chatJid: string;
  messageId?: string;
  isScheduledTask?: boolean;
  assistantName?: string;
  secrets?: Record<string, string>;
  soul?: string;
  systemMd?: string;
}

interface ContainerOutput {
  status: 'success' | 'error';
  result: string | null;
  newSessionId?: string;
  error?: string;
  // Per-model usage harvested from the SDK's result message (Anthropic
  // usage only; oracle/codex skill captures separately). Spec 5/34.
  models?: Record<string, import('./mcp.js').ModelUsage>;
  // timedOut marks a result that is the graceful query-timeout summary.
  timedOut?: boolean;
}

const HOME = '/home/node';
const IPC_INPUT_DIR = '/run/ipc/input';
const IPC_INPUT_CLOSE_SENTINEL = path.join(IPC_INPUT_DIR, '_close');
const IPC_POLL_MS = 500;
const MAX_STDIN_BYTES = 1024 * 1024;

const PROGRESS_INTERVAL_MS = 90_000;

function buildSystemPrompt(ci: ContainerInput):
    string | { type: 'preset'; preset: 'claude_code' } {
  const parts = [ci.systemMd, ci.soul, readOutputStyle()].filter(Boolean);
  if (parts.length > 0) return parts.join('\n\n');
  return { type: 'preset' as const, preset: 'claude_code' as const };
}

function readOutputStyle(): string | null {
  try {
    const s = JSON.parse(fs.readFileSync(`${HOME}/.claude/settings.json`, 'utf-8'));
    const name = s.outputStyle;
    if (!name || name === 'default') return null;
    const raw = fs.readFileSync(`${HOME}/.claude/output-styles/${name}.md`, 'utf-8');
    return raw.replace(/^---\n[\s\S]*?\n---\n*/, '').trim() || null;
  } catch {
    return null;
  }
}

let wakeup: (() => void) | null = null;
process.on('SIGUSR1', () => { if (wakeup) wakeup(); });

async function readStdin(): Promise<string> {
  return new Promise((resolve, reject) => {
    let data = '';
    let size = 0;
    let aborted = false;
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', chunk => {
      if (aborted) return;
      size += Buffer.byteLength(chunk, 'utf8');
      if (size > MAX_STDIN_BYTES) {
        aborted = true;
        process.stdin.pause();
        reject(new Error(`stdin exceeds max size (${MAX_STDIN_BYTES} bytes)`));
        return;
      }
      data += chunk;
    });
    process.stdin.on('end', () => { if (!aborted) resolve(data); });
    process.stdin.on('error', reject);
  });
}

async function deliverTurn(turnID: string, output: ContainerOutput): Promise<void> {
  if (!turnID) {
    log(`deliverTurn skipped: no turn_id (status=${output.status})`);
    return;
  }
  try {
    await submitTurn({
      turn_id: turnID,
      session_id: output.newSessionId,
      status: output.status,
      result: output.result ?? undefined,
      error: output.error,
      models: output.models,
      timed_out: output.timedOut,
    });
  } catch (err) {
    log(`submit_turn failed: ${err instanceof Error ? err.message : String(err)}`);
  }
}

function log(message: string): void {
  console.error(`[ant] ${message}`);
}

function nudgeProgress(): void {
  const fp = path.join(IPC_INPUT_DIR, `${Date.now()}-progress.json`);
  const payload = JSON.stringify({
    type: 'message',
    source: 'nudge',
    text: 'Report progress to the user now. Use <status>short summary of what you are doing</status>.',
  });
  try {
    fs.mkdirSync(IPC_INPUT_DIR, { recursive: true });
    fs.writeFileSync(fp + '.tmp', payload);
    fs.renameSync(fp + '.tmp', fp);
    log('Nudged agent for progress report');
  } catch (err) {
    log(`Progress nudge write failed: ${err instanceof Error ? err.message : String(err)}`);
  }
}

// STATUS_RE matches one <status>...</status> block. The agent emits these for
// interim progress (ant/CLAUDE.md "Status updates"); we forward each mid-turn so
// the user sees ⏳ progress on a long turn instead of nothing until the end.
const STATUS_RE = /<status>([\s\S]*?)<\/status>/g;

// assistantText flattens an SDK assistant message's text parts, mirroring
// parseTranscript's assistant branch.
function assistantText(raw: Record<string, unknown>): string {
  const content = (raw as { message?: { content?: unknown } }).message?.content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((p: { type?: string }) => p.type === 'text')
    .map((p: { text?: string }) => p.text || '')
    .join('');
}

// streamStatuses forwards any <status> blocks in an intermediate assistant
// message to routd (delivered immediately as ⏳ interim notices). seen dedups
// across the turn so a status that also lands in the final result isn't
// double-delivered — the final result flows through submit_turn separately.
async function streamStatuses(turnID: string, text: string, seen: Set<string>): Promise<void> {
  if (!turnID) return;
  for (const m of text.matchAll(STATUS_RE)) {
    const s = m[1].trim();
    if (!s || seen.has(s)) continue;
    seen.add(s);
    try {
      await submitStatus({ turn_id: turnID, text: s });
    } catch (err) {
      log(`submit_status failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
}

function shouldClose(): boolean {
  if (fs.existsSync(IPC_INPUT_CLOSE_SENTINEL)) {
    try { fs.unlinkSync(IPC_INPUT_CLOSE_SENTINEL); } catch { /* ignore */ }
    return true;
  }
  return false;
}

function drainIpcInput(): string[] {
  try {
    fs.mkdirSync(IPC_INPUT_DIR, { recursive: true });
    const files = fs.readdirSync(IPC_INPUT_DIR)
      .filter(f => f.endsWith('.json'))
      .sort();

    const messages: string[] = [];
    for (const file of files) {
      const filePath = path.join(IPC_INPUT_DIR, file);
      try {
        const data = JSON.parse(fs.readFileSync(filePath, 'utf-8'));
        fs.unlinkSync(filePath);
        if (data.source === 'self' || data.source === 'nudge') continue;
        if (data.type === 'message' && typeof data.text === 'string') {
          messages.push(data.text);
        }
      } catch (err) {
        log(`Failed to process input file ${file}: ${err instanceof Error ? err.message : String(err)}`);
        try { fs.unlinkSync(filePath); } catch { /* ignore */ }
      }
    }
    return messages;
  } catch (err) {
    log(`IPC drain error: ${err instanceof Error ? err.message : String(err)}`);
    return [];
  }
}

function discardNudges(): number {
  try {
    fs.mkdirSync(IPC_INPUT_DIR, { recursive: true });
    const files = fs.readdirSync(IPC_INPUT_DIR)
      .filter(f => f.endsWith('.json'))
      .sort();
    const toDelete: string[] = [];
    for (const file of files) {
      const fp = path.join(IPC_INPUT_DIR, file);
      try {
        const data = JSON.parse(fs.readFileSync(fp, 'utf-8'));
        if (data.source === 'nudge') toDelete.push(fp);
      } catch { /* skip unreadable files */ }
    }
    let count = 0;
    for (const fp of toDelete) {
      try { fs.unlinkSync(fp); count++; } catch { /* ignore */ }
    }
    return count;
  } catch { return 0; }
}

function checkIpcMessage(): string | null {
  if (shouldClose()) return null;
  const messages = drainIpcInput();
  return messages.length > 0 ? messages.join('\n') : null;
}

// buildSessionConfig assembles the backend-neutral SessionConfig from the
// container input + the active backend's MCP rendering. extraDirs and the
// assembled MCP server map carry the same values the pre-seam runtime used.
function buildSessionConfig(
  backend: Backend,
  prompt: string,
  sessionId: string | undefined,
  containerInput: ContainerInput,
  sdkEnv: Record<string, string | undefined>,
  turnID: string,
  resumeAt?: string,
): SessionConfig {
  const extraDirs: string[] = [];
  // 4/R: root is an operator /root elevation, NOT a folder shape — a top-level world
  // has no slash but is NOT root. Read the elevation marker the gateway sets, never
  // derive it from the path (a named world would wrongly skip /var/lib/share).
  const isRoot = process.env.ARIZUKO_IS_ROOT === '1';
  if (!isRoot && fs.existsSync('/var/lib/share')) extraDirs.push('/var/lib/share');
  try {
    for (const e of fs.readdirSync('/mnt')) {
      const p = path.join('/mnt', e);
      if (fs.statSync(p).isDirectory()) extraDirs.push(p);
    }
  } catch { /* /mnt absent */ }

  const agentMcpServers = loadAgentMcpServers(HOME);
  return {
    prompt,
    model: sdkEnv['ARIZUKO_MODEL'] || undefined,
    cwd: HOME,
    resume: sessionId,
    resumeAt,
    systemPrompt: buildSystemPrompt(containerInput),
    addDirs: extraDirs,
    env: sdkEnv,
    mcpServers: backend.name() === 'claude'
      ? renderMcpServers(agentMcpServers, sdkEnv)
      : agentMcpServers,
    assistantName: containerInput.assistantName,
    turnID,
  };
}

async function runQuery(
  backend: Backend,
  prompt: string,
  sessionId: string | undefined,
  containerInput: ContainerInput,
  sdkEnv: Record<string, string | undefined>,
  turnID: string,
  resumeAt?: string,
): Promise<{ newSessionId?: string; lastAssistantUuid?: string; closedDuringQuery: boolean; sessionError: boolean }> {
  const cfg = buildSessionConfig(backend, prompt, sessionId, containerInput, sdkEnv, turnID, resumeAt);
  const session: Session = await backend.spawn(cfg);

  let ipcPolling = true;
  let closedDuringQuery = false;
  const pollIpcDuringQuery = () => {
    if (!ipcPolling) return;
    if (shouldClose()) {
      log('Close sentinel detected during query, ending stream');
      closedDuringQuery = true;
      void session.close();
      ipcPolling = false;
      wakeup = null;
      return;
    }
    let timer: ReturnType<typeof setTimeout>;
    wakeup = () => { clearTimeout(timer); pollIpcDuringQuery(); };
    timer = setTimeout(pollIpcDuringQuery, IPC_POLL_MS);
  };
  setTimeout(pollIpcDuringQuery, IPC_POLL_MS);

  let newSessionId: string | undefined;
  let lastAssistantUuid: string | undefined;
  let messageCount = 0;
  let resultCount = 0;
  let sessionError = false;
  let lastProgressAt = Date.now();
  const seenStatuses = new Set<string>();

  try {
    for await (const event of session.events()) {
      messageCount++;
      log(`[msg #${messageCount}] type=${event.type}`);

      if (event.type === 'assistant') {
        const uuid = (event.raw as { uuid?: string }).uuid;
        if (uuid) lastAssistantUuid = uuid;
        await streamStatuses(turnID, assistantText(event.raw), seenStatuses);
      }

      const now = Date.now();
      if (now - lastProgressAt >= PROGRESS_INTERVAL_MS || messageCount % 500 === 0) {
        nudgeProgress();
        lastProgressAt = now;
      }

      if (event.type === 'system_init') {
        newSessionId = event.sessionId;
        log(`Session initialized: ${newSessionId}`);
      }

      if (event.type === 'result') {
        resultCount++;
        const subtype = (event.raw as { subtype?: string }).subtype;
        const textResult = event.text ?? null;
        const models = event.models;
        log(`Result #${resultCount}: subtype=${subtype}${textResult ? ` text=${textResult.slice(0, 200)}` : ''}${models ? ` models=${Object.keys(models).join(',')}` : ''}`);
        if (isSessionError(subtype)) {
          log('Session error, will retry without session');
          sessionError = true;
        } else {
          await deliverTurn(turnID, { status: event.status ?? 'success', result: textResult || null, newSessionId, models, timedOut: event.timedOut });
        }
      }
    }
  } catch (err) {
    if (resultCount > 0) {
      log(`SDK threw after result (ignored): ${err instanceof Error ? err.message : String(err)}`);
    } else {
      await session.close();
      throw err;
    }
  }

  ipcPolling = false;
  wakeup = null;
  await session.close();

  const discarded = discardNudges();
  if (discarded > 0) {
    log(`Discarded ${discarded} stale progress nudges after query`);
  }

  log(`Query done. Messages: ${messageCount}, results: ${resultCount}, lastAssistantUuid: ${lastAssistantUuid || 'none'}, closedDuringQuery: ${closedDuringQuery}`);

  return { newSessionId, lastAssistantUuid, closedDuringQuery, sessionError };
}

async function main(): Promise<void> {
  let containerInput!: ContainerInput;

  let privateTmpDir: string | null = null;
  try {
    privateTmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'arizuko-'));
    try { fs.chmodSync(privateTmpDir, 0o700); } catch { /* ignore */ }
  } catch { /* ignore */ }

  const cleanupSecrets = () => {
    try { fs.unlinkSync('/tmp/input.json'); } catch { /* may not exist */ }
    if (privateTmpDir) {
      try { fs.rmSync(privateTmpDir, { recursive: true, force: true }); } catch { /* ignore */ }
    }
  };

  try {
    try {
      const stdinData = await readStdin();
      containerInput = JSON.parse(stdinData);
      log(`Received input for group: ${containerInput.groupFolder}`);
    } catch (err) {
      cleanupSecrets();
      log(`Failed to parse input: ${err instanceof Error ? err.message : String(err)}`);
      process.exit(1);
    }

    const sdkEnv: Record<string, string | undefined> = { ...process.env };
    for (const [key, value] of Object.entries(containerInput.secrets || {})) {
      sdkEnv[key] = value;
    }

    // ARIZUKO_BACKEND picks the harness; default "claude", unknown = fatal.
    const backend = selectBackend(process.env.ARIZUKO_BACKEND, drainIpcInput);
    log(`Backend: ${backend.name()}`);

    // Resolve the resume target: a lineage placeholder (non-UUID) or a UUID whose
    // transcript is gone (pruned / never persisted) both start fresh silently,
    // never surfacing error_during_execution as a delivered result.
    let sessionId = resolveResumeSession(
      backend.name(),
      containerInput.sessionId,
      id => fs.existsSync(transcriptPath(id)),
    );
    fs.mkdirSync(IPC_INPUT_DIR, { recursive: true });

    try { fs.unlinkSync(IPC_INPUT_CLOSE_SENTINEL); } catch { /* ignore */ }

    let prompt = containerInput.prompt;
    if (containerInput.isScheduledTask) {
      prompt = `[SCHEDULED TASK - The following message was sent automatically and is not coming directly from the user or group.]\n\n${prompt}`;
    }
    if (containerInput.soul && !containerInput.systemMd) {
      prompt = containerInput.soul + '\n\n' + prompt;
    }
    const pending = drainIpcInput();
    if (pending.length > 0) {
      log(`Draining ${pending.length} pending IPC messages into initial prompt`);
      prompt += '\n' + pending.join('\n');
    }

    const seedTurnID = containerInput.messageId || `boot-${Date.now()}`;
    let turnIndex = 0;
    let resumeAt: string | undefined;
    try {
      while (true) {
        log(`Starting query (session: ${sessionId || 'new'}, resumeAt: ${resumeAt || 'latest'})...`);

        const turnID = turnIndex === 0 ? seedTurnID : `${seedTurnID}:${turnIndex}`;
        const queryResult = await runQuery(backend, prompt, sessionId, containerInput, sdkEnv, turnID, resumeAt);
        if (queryResult.sessionError && sessionId) {
          log(`Session error on resume, retrying with fresh session (was: ${sessionId})`);
          sessionId = undefined;
          resumeAt = undefined;
          continue;
        }
        if (queryResult.newSessionId) {
          sessionId = queryResult.newSessionId;
        }
        if (queryResult.lastAssistantUuid) {
          resumeAt = queryResult.lastAssistantUuid;
        }

        if (queryResult.closedDuringQuery) {
          log('Close sentinel consumed during query, exiting');
          break;
        }

        log('Query ended, checking for next IPC message...');

        const nextMessage = checkIpcMessage();
        if (nextMessage === null) {
          log('Input empty, exiting');
          break;
        }

        turnIndex++;
        log(`Got new message (${nextMessage.length} chars), starting new query (turn ${turnIndex})`);
        prompt = nextMessage;
      }
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      log(`Agent error: ${errorMessage}`);
      const turnID = turnIndex === 0 ? seedTurnID : `${seedTurnID}:${turnIndex}`;
      await deliverTurn(turnID, {
        status: 'error',
        result: null,
        newSessionId: sessionId,
        error: errorMessage,
      });
      process.exit(1);
    }
  } finally {
    cleanupSecrets();
  }
}

// Run main only as the process entrypoint (`node dist/index.js`), so tests can
// import the exported helpers without kicking off the stdin-reading runtime.
function isEntrypoint(): boolean {
  if (!process.argv[1]) return false;
  try {
    return fs.realpathSync(fileURLToPath(import.meta.url)) === fs.realpathSync(process.argv[1]);
  } catch {
    return false;
  }
}

if (isEntrypoint()) {
  main().catch((err) => {
    const errorMessage = err instanceof Error ? err.message : String(err);
    log(`Unhandled error: ${errorMessage}`);
    process.exit(1);
  });
}
