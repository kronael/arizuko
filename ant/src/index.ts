import fs from 'fs';
import os from 'os';
import path from 'path';
import { query, HookCallback, PreCompactHookInput, PreToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';
import { submitTurn } from './mcp.js';

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

interface SessionEntry {
  sessionId: string;
  summary: string;
}

interface SDKUserMessage {
  type: 'user';
  message: { role: 'user'; content: string };
  parent_tool_use_id: null;
  session_id: string;
}

type McpServerConfig = { command: string; args?: string[]; env?: Record<string, string> };

const HOME = '/home/node';
const IPC_INPUT_DIR = '/workspace/ipc/input';
const IPC_INPUT_CLOSE_SENTINEL = path.join(IPC_INPUT_DIR, '_close');
const IPC_POLL_MS = 500;
const MAX_QUEUE = 100;
const MAX_STDIN_BYTES = 1024 * 1024;
const QUERY_TIMEOUT_MS = 15 * 60_000;

const PROGRESS_INTERVAL_MS = 15 * 60_000;

function loadAgentMcpServers(): Record<string, McpServerConfig> {
  try {
    const s = JSON.parse(fs.readFileSync(`${HOME}/.claude/settings.json`, 'utf-8'));
    const servers = s.mcpServers;
    if (!servers || typeof servers !== 'object') return {};
    delete servers.arizuko;
    return servers;
  } catch {
    return {};
  }
}

// Inject secrets into each MCP server's env so spawned MCP processes inherit them.
function injectMcpEnv(
  servers: Record<string, McpServerConfig>,
  secrets: Record<string, string | undefined>,
): Record<string, McpServerConfig> {
  const definedSecrets: Record<string, string> = {};
  for (const [k, v] of Object.entries(secrets)) {
    if (v !== undefined) definedSecrets[k] = v;
  }
  const out: Record<string, McpServerConfig> = {};
  for (const [name, cfg] of Object.entries(servers)) {
    out[name] = { ...cfg, env: { ...cfg.env, ...definedSecrets } };
  }
  // Always include arizuko MCP (socat to gated socket).
  out.arizuko = {
    command: 'socat',
    args: ['STDIO', 'UNIX-CONNECT:/workspace/ipc/gated.sock'],
    env: definedSecrets,
  };
  return out;
}

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

// extractModelUsage converts the SDK's modelUsage record to the snake_case
// shape gated expects. costUSD → cost_cents via × 100 + round. Spec 5/34.
function extractModelUsage(modelUsage: unknown): Record<string, import('./mcp.js').ModelUsage> | undefined {
  if (!modelUsage || typeof modelUsage !== 'object') return undefined;
  const out: Record<string, import('./mcp.js').ModelUsage> = {};
  for (const [model, u] of Object.entries(modelUsage as Record<string, {
    inputTokens?: number;
    outputTokens?: number;
    cacheReadInputTokens?: number;
    cacheCreationInputTokens?: number;
    costUSD?: number;
  }>)) {
    out[model] = {
      input_tokens: u.inputTokens ?? 0,
      output_tokens: u.outputTokens ?? 0,
      cache_read_input_tokens: u.cacheReadInputTokens ?? 0,
      cache_creation_input_tokens: u.cacheCreationInputTokens ?? 0,
      cost_cents: Math.round((u.costUSD ?? 0) * 100),
    };
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function log(message: string): void {
  console.error(`[ant] ${message}`);
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

const SECRET_ENV_VARS = ['ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN'];

function createSanitizeBashHook(): HookCallback {
  return async (input, _toolUseId, _context) => {
    const preInput = input as PreToolUseHookInput;
    const command = (preInput.tool_input as { command?: string })?.command;
    if (!command) return {};

    const unsetPrefix = `unset ${SECRET_ENV_VARS.join(' ')} 2>/dev/null; `;
    return {
      hookSpecificOutput: {
        hookEventName: 'PreToolUse',
        updatedInput: {
          ...(preInput.tool_input as Record<string, unknown>),
          command: unsetPrefix + command,
        },
      },
    };
  };
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

function createIpcDrainHook(): HookCallback {
  return async (_input, _toolUseId, _context) => {
    const messages = drainIpcInput();
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

function checkIpcMessage(): string | null {
  if (shouldClose()) return null;
  const messages = drainIpcInput();
  return messages.length > 0 ? messages.join('\n') : null;
}

async function runQuery(
  prompt: string,
  sessionId: string | undefined,
  containerInput: ContainerInput,
  sdkEnv: Record<string, string | undefined>,
  turnID: string,
  resumeAt?: string,
): Promise<{ newSessionId?: string; lastAssistantUuid?: string; closedDuringQuery: boolean; sessionError: boolean }> {
  const stream = new MessageStream();
  stream.push(prompt);

  let ipcPolling = true;
  let closedDuringQuery = false;
  const pollIpcDuringQuery = () => {
    if (!ipcPolling) return;
    if (shouldClose()) {
      log('Close sentinel detected during query, ending stream');
      closedDuringQuery = true;
      stream.end();
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
  let maxTurnsHit = false;
  let sessionError = false;
  let lastProgressAt = Date.now();

  const extraDirs: string[] = [];
  const isRoot = !containerInput.groupFolder.includes('/');
  if (!isRoot && fs.existsSync('/workspace/share')) extraDirs.push('/workspace/share');
  try {
    for (const e of fs.readdirSync('/workspace/extra')) {
      const p = path.join('/workspace/extra', e);
      if (fs.statSync(p).isDirectory()) extraDirs.push(p);
    }
  } catch { /* /workspace/extra absent */ }

  const agentMcpServers = loadAgentMcpServers();

  const abortController = new AbortController();
  const timeoutTimer = setTimeout(() => {
    log(`Query timeout (${QUERY_TIMEOUT_MS}ms) reached, aborting`);
    abortController.abort();
    stream.end();
  }, QUERY_TIMEOUT_MS);

  try {
    for await (const message of query({
      prompt: stream,
      options: {
        abortController,
        cwd: HOME,
        additionalDirectories: extraDirs.length > 0 ? extraDirs : undefined,
        resume: sessionId,
        resumeSessionAt: resumeAt,
        systemPrompt: buildSystemPrompt(containerInput),
        allowedTools: [
          'Bash',
          'Read', 'Write', 'Edit', 'Glob', 'Grep',
          'WebSearch', 'WebFetch',
          'Task', 'TaskOutput', 'TaskStop',
          'TodoWrite', 'ToolSearch', 'Skill',
          'NotebookEdit',
          'mcp__arizuko__*',
          ...Object.keys(agentMcpServers).map((n) => `mcp__${n}__*`),
        ],
        env: sdkEnv,
        permissionMode: 'bypassPermissions',
        allowDangerouslySkipPermissions: true,
        settingSources: ['project', 'user'],
        mcpServers: injectMcpEnv(agentMcpServers, sdkEnv),
        hooks: {
          PreCompact: [{ hooks: [createPreCompactHook(containerInput.assistantName)] }],
          PreToolUse: [{ matcher: 'Bash', hooks: [createSanitizeBashHook()] }],
          PostToolUse: [{ hooks: [createIpcDrainHook()] }],
        },
      }
    })) {
      messageCount++;
      const msgType = message.type === 'system' ? `system/${(message as { subtype?: string }).subtype}` : message.type;
      log(`[msg #${messageCount}] type=${msgType}`);

      if (message.type === 'assistant') {
        const uuid = (message as { uuid?: string }).uuid;
        if (uuid) lastAssistantUuid = uuid;
      }

      const now = Date.now();
      if (now - lastProgressAt >= PROGRESS_INTERVAL_MS || messageCount % 500 === 0) {
        nudgeProgress();
        lastProgressAt = now;
      }

      if (message.type === 'system' && message.subtype === 'init') {
        newSessionId = message.session_id;
        log(`Session initialized: ${newSessionId}`);
      }

      if (message.type === 'system' && (message as { subtype?: string }).subtype === 'task_notification') {
        const tn = message as { task_id: string; status: string; summary: string };
        log(`Task notification: task=${tn.task_id} status=${tn.status} summary=${tn.summary}`);
      }

      if (message.type === 'result') {
        resultCount++;
        const textResult = 'result' in message ? (message as { result?: string }).result : null;
        const models = extractModelUsage((message as { modelUsage?: unknown }).modelUsage);
        log(`Result #${resultCount}: subtype=${message.subtype}${textResult ? ` text=${textResult.slice(0, 200)}` : ''}${models ? ` models=${Object.keys(models).join(',')}` : ''}`);
        stream.end();
        if (message.subtype === 'error_max_turns') {
          maxTurnsHit = true;
        } else if (message.subtype === 'error_during_execution') {
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
      clearTimeout(timeoutTimer);
      throw err;
    }
  }

  clearTimeout(timeoutTimer);
  ipcPolling = false;
  wakeup = null;

  const discarded = discardNudges();
  if (discarded > 0) {
    log(`Discarded ${discarded} stale progress nudges after query`);
  }

  log(`Query done. Messages: ${messageCount}, results: ${resultCount}, lastAssistantUuid: ${lastAssistantUuid || 'none'}, closedDuringQuery: ${closedDuringQuery}`);

  if (maxTurnsHit && newSessionId) {
    log('Max turns hit; requesting summary + resumption nudge');
    for await (const msg of query({
      prompt: 'You ran out of turns mid-task. Summarise concisely: what you accomplished, what is still pending. Then tell the user they can say "continue" to resume where you left off.',
      options: {
        cwd: HOME,
        maxTurns: 3,
        resume: newSessionId,
        permissionMode: 'bypassPermissions' as const,
        allowDangerouslySkipPermissions: true,
      },
    })) {
      if (msg.type === 'result') {
        const txt = (msg as { result?: string }).result ?? null;
        await deliverTurn(turnID, { status: 'success', result: txt ?? 'ran out of turns; say "continue" to resume.', newSessionId });
      }
    }
  }

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
        const queryResult = await runQuery(prompt, sessionId, containerInput, sdkEnv, turnID, resumeAt);
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
