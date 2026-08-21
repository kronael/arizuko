// index.ts — the chassis runtime half every occupant shares: stdin envelope
// parse, IPC steering + close sentinel, the <status> pipeline, idempotent
// submit_turn, the inner turn loop. The harness half lives behind the seam in
// backend.ts (spec 6/7 § "The harness seam").

import fs from 'fs';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { submitTurn, submitStatus } from './mcp.js';
import { injectMcpEnv } from './mcp-servers.js';
import { Harness, Turn, selectHarness } from './backend.js';
import { STEER_FILE, appendLines, drainLines } from './spool.js';

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
  // Per-model usage harvested from the harness's result frame (spec 11/19).
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

// buildSystemPrompt renders the text layers the chassis owns (system MD, soul,
// output style). undefined means "no rendered text" — each harness supplies
// its own default (claude's claude_code preset is claude wiring, not seam).
function buildSystemPrompt(ci: ContainerInput): string | undefined {
  const parts = [ci.systemMd, ci.soul, readOutputStyle()].filter(Boolean);
  return parts.length > 0 ? parts.join('\n\n') : undefined;
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

// statusBlocks extracts the trimmed non-empty <status> blocks from one chunk
// of interim assistant text. THE one extraction site: every harness's status
// frame flows through here — before the seam inversion this ran on claude's
// raw message shape only, so codex streamed no interim status at all.
export function statusBlocks(text: string): string[] {
  const out: string[] = [];
  for (const m of text.matchAll(STATUS_RE)) {
    const s = m[1].trim();
    if (s) out.push(s);
  }
  return out;
}

// streamStatuses forwards <status> blocks from a status frame to routd
// (delivered immediately as ⏳ interim notices). seen dedups across the turn
// so a status that also lands in the final result isn't double-delivered —
// the final result flows through submit_turn separately.
async function streamStatuses(turnID: string, text: string, seen: Set<string>): Promise<void> {
  if (!turnID) return;
  for (const s of statusBlocks(text)) {
    if (seen.has(s)) continue;
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

interface TurnResult {
  // token is the harness's latest continuity token, opaque to us — handed to
  // the next run() verbatim.
  token?: string;
  // sessionId is the harness-native session id for routd's lineage row.
  sessionId?: string;
  closedDuringQuery: boolean;
  retryFresh: boolean;
}

// runTurn drives one harness run and consumes its frames: status frames feed
// the <status> pipeline, continuity frames update the resume state, the result
// frame is delivered. retryFresh results are never delivered — the caller
// retries with no continuity.
async function runTurn(
  harness: Harness,
  prompt: string,
  continuity: string | undefined,
  containerInput: ContainerInput,
  env: Record<string, string>,
  turnID: string,
): Promise<TurnResult> {
  const controller = new AbortController();
  const turn: Turn = {
    prompt,
    cwd: HOME,
    turnID,
    model: env.ARIZUKO_MODEL || undefined,
    continuity,
    systemPrompt: buildSystemPrompt(containerInput),
    tools: injectMcpEnv(env),
    env,
    signal: controller.signal,
  };

  let ipcPolling = true;
  let closedDuringQuery = false;
  const pollIpcDuringQuery = () => {
    if (!ipcPolling) return;
    // The chassis owns the IPC mechanism; the `steering` policy owns delivery.
    // Queueing here rather than inside a hook is what lets codex reach it — its
    // hooks are separate processes and cannot be handed a closure.
    appendLines(STEER_FILE, drainIpcInput());
    if (shouldClose()) {
      log('Close sentinel detected during query, winding the turn down');
      closedDuringQuery = true;
      controller.abort();
      ipcPolling = false;
      wakeup = null;
      return;
    }
    let timer: ReturnType<typeof setTimeout>;
    wakeup = () => { clearTimeout(timer); pollIpcDuringQuery(); };
    timer = setTimeout(pollIpcDuringQuery, IPC_POLL_MS);
  };
  setTimeout(pollIpcDuringQuery, IPC_POLL_MS);

  // The nudge asks the agent for a <status> report; the steering drain feeds
  // it into the active query (claude's mid-turn gate; discarded after the run
  // for harnesses that only steer between turns). Wall-clock, so a long
  // tool-heavy stretch still nudges even when no frame arrives.
  const nudgeTimer = setInterval(nudgeProgress, PROGRESS_INTERVAL_MS);

  const out: TurnResult = { closedDuringQuery: false, retryFresh: false };
  let frameCount = 0;
  let resultCount = 0;
  const seenStatuses = new Set<string>();

  try {
    for await (const frame of harness.run(turn)) {
      frameCount++;
      log(`[frame #${frameCount}] kind=${frame.kind}`);

      if (frame.kind === 'continuity') {
        out.token = frame.token;
        if (!out.sessionId) log(`Session initialized: ${frame.sessionId}`);
        out.sessionId = frame.sessionId;
      } else if (frame.kind === 'status') {
        await streamStatuses(turnID, frame.text, seenStatuses);
      } else {
        resultCount++;
        log(`Result #${resultCount}: ok=${frame.ok}${frame.retryFresh ? ' retryFresh' : ''}${frame.text ? ` text=${frame.text.slice(0, 200)}` : ''}${frame.usage ? ` models=${Object.keys(frame.usage).join(',')}` : ''}`);
        if (frame.retryFresh) {
          log('Session error, will retry without session');
          out.retryFresh = true;
        } else {
          await deliverTurn(turnID, {
            status: frame.ok ? 'success' : 'error',
            result: frame.text || null,
            newSessionId: out.sessionId,
            models: frame.usage,
            timedOut: frame.timedOut,
          });
        }
      }
    }
  } catch (err) {
    if (resultCount > 0) {
      log(`Harness threw after result (ignored): ${err instanceof Error ? err.message : String(err)}`);
    } else {
      throw err;
    }
  } finally {
    ipcPolling = false;
    wakeup = null;
    clearInterval(nudgeTimer);
  }

  const discarded = discardNudges();
  if (discarded > 0) {
    log(`Discarded ${discarded} stale progress nudges after query`);
  }

  out.closedDuringQuery = closedDuringQuery;
  log(`Turn done. Frames: ${frameCount}, results: ${resultCount}, sessionId: ${out.sessionId || 'none'}, closedDuringQuery: ${closedDuringQuery}`);
  return out;
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

    // The seam's env is Record<string,string>: process.env filtered once here
    // instead of once per harness.
    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(process.env)) {
      if (v !== undefined) env[k] = v;
    }
    for (const [key, value] of Object.entries(containerInput.secrets || {})) {
      env[key] = value;
    }

    fs.mkdirSync(IPC_INPUT_DIR, { recursive: true });
    try { fs.unlinkSync(IPC_INPUT_CLOSE_SENTINEL); } catch { /* ignore */ }

    let prompt = containerInput.prompt;
    if (containerInput.isScheduledTask) {
      prompt = `[SCHEDULED TASK - The following message was sent automatically and is not coming directly from the user or group.]\n\n${prompt}`;
    }
    if (containerInput.soul && !containerInput.systemMd) {
      prompt = containerInput.soul + '\n\n' + prompt;
    }
    // Both queues: what IPC holds now, plus anything the steering policy never
    // got to deliver because the last turn stopped calling tools.
    const pending = [...drainLines(STEER_FILE), ...drainIpcInput()];
    if (pending.length > 0) {
      log(`Draining ${pending.length} pending IPC messages into initial prompt`);
      prompt += '\n' + pending.join('\n');
    }

    // The platform hands back the sessionId a continuity frame carried; it is
    // a valid token for the harness that minted it (claude/codex accept a bare
    // id; pi ignores continuity). The harness unpacks and validates — a
    // placeholder or a pruned transcript starts fresh inside run().
    let continuity = containerInput.sessionId;
    let lastSessionId = containerInput.sessionId;
    const seedTurnID = containerInput.messageId || `boot-${Date.now()}`;
    let turnIndex = 0;
    try {
      // Selected inside the delivering try: an unknown ARIZUKO_BACKEND fails
      // this turn loudly to the user, never a silent exit (spec 6/7).
      const harness = selectHarness(process.env.ARIZUKO_BACKEND);
      log(`backend: ${harness.name}`);
      while (true) {
        log(`Starting turn (continuity: ${continuity || 'fresh'})...`);

        const turnID = turnIndex === 0 ? seedTurnID : `${seedTurnID}:${turnIndex}`;
        const r = await runTurn(harness, prompt, continuity, containerInput, env, turnID);
        if (r.retryFresh && continuity) {
          log(`Session error on resume, retrying with fresh session (was: ${continuity})`);
          continuity = undefined;
          continue;
        }
        if (r.token) continuity = r.token;
        if (r.sessionId) lastSessionId = r.sessionId;

        if (r.closedDuringQuery) {
          log('Close sentinel consumed during query, exiting');
          break;
        }

        log('Turn ended, checking for next IPC message...');

        const nextMessage = checkIpcMessage();
        if (nextMessage === null) {
          log('Input empty, exiting');
          break;
        }

        turnIndex++;
        log(`Got new message (${nextMessage.length} chars), starting new turn (turn ${turnIndex})`);
        prompt = nextMessage;
      }
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      log(`Agent error: ${errorMessage}`);
      const turnID = turnIndex === 0 ? seedTurnID : `${seedTurnID}:${turnIndex}`;
      await deliverTurn(turnID, {
        status: 'error',
        result: null,
        newSessionId: lastSessionId,
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
