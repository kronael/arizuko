import fs from 'fs';
import os from 'os';
import path from 'path';
import { submitTurn } from './mcp.js';
import { loadAgentMcpServers } from './mcp-servers.js';
import { Backend, Session, SessionConfig, selectBackend, renderMcpServers } from './backend/index.js';

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
}

const HOME = '/home/node';
const IPC_INPUT_DIR = '/run/ipc/input';
const IPC_INPUT_CLOSE_SENTINEL = path.join(IPC_INPUT_DIR, '_close');
const IPC_POLL_MS = 500;
const MAX_STDIN_BYTES = 1024 * 1024;

const PROGRESS_INTERVAL_MS = 15 * 60_000;

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
  resumeAt?: string,
): SessionConfig {
  const extraDirs: string[] = [];
  const isRoot = !containerInput.groupFolder.includes('/');
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
  const cfg = buildSessionConfig(backend, prompt, sessionId, containerInput, sdkEnv, resumeAt);
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

  try {
    for await (const event of session.events()) {
      messageCount++;
      log(`[msg #${messageCount}] type=${event.type}`);

      if (event.type === 'assistant') {
        const uuid = (event.raw as { uuid?: string }).uuid;
        if (uuid) lastAssistantUuid = uuid;
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
        if (subtype === 'error_during_execution') {
          log('Session error, will retry without session');
          sessionError = true;
        } else {
          await deliverTurn(turnID, { status: 'success', result: textResult || null, newSessionId, models });
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

    let sessionId = containerInput.sessionId;
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

main().catch((err) => {
  const errorMessage = err instanceof Error ? err.message : String(err);
  log(`Unhandled error: ${errorMessage}`);
  process.exit(1);
});
