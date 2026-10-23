// index.ts — the chassis runtime half every occupant shares: stdin envelope
// parse, IPC steering + close sentinel, the <status> pipeline, idempotent
// submit_turn, the inner turn loop. The harness half lives behind backend.ts.

import fs from 'fs';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { submitTurn, submitStatus, type TurnAck } from './mcp.js';
import { injectMcpEnv } from './mcp-servers.js';
import { Harness, HarnessProfile, Turn, selectHarness } from './backend.js';
import { STEER_SPOOL, appendLines, drainLines } from './spool.js';

interface ContainerInput {
  prompt: string;
  sessionId?: string;
  groupFolder: string;
  chatJid: string;
  messageId?: string;
  isScheduledTask?: boolean;
  assistantName?: string;
  secrets?: Record<string, string>;
  systemMd?: string;
  persona?: string;
}

interface ContainerOutput {
  status: 'success' | 'error';
  newSessionId?: string;
  error?: string;
  // Per-model usage harvested from the harness's result frame (spec 6/39).
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

// runed hands the guest ARIZUKO_QUERY_TIMEOUT_MS under its own run ceiling. The
// guest's clock starts with this process, so one deadline covers every harness
// run in it, the nudge round and the next IPC turn included.
const PROCESS_DEADLINE = Date.now() + (Number(process.env.ARIZUKO_QUERY_TIMEOUT_MS) || 15 * 60_000);

// buildSystemPrompt renders the text layers the chassis owns (system MD, the
// resident manual, persona, output style). undefined means "no rendered text"
// — each harness supplies its own default.
export function buildSystemPrompt(
  ci: ContainerInput,
  profile: Pick<HarnessProfile, 'manual'>,
  manual: () => string | null = readHomeManual,
): string | undefined {
  // SYSTEM.md replaces the harness's own base prompt; PERSONA.md appends to
  // it, so a group can add voice without restating the platform contract.
  // runner.go reads both from the group dir and ships them on the same wire.
  // The manual sits between them: it is the platform contract the persona
  // adds voice on top of, and a harness that reads the file natively must
  // not receive it a second time here.
  const parts = [
    ci.systemMd,
    profile.manual === 'rendered' ? manual() : null,
    ci.persona,
    readOutputStyle(),
  ].filter(Boolean);
  return parts.length > 0 ? parts.join('\n\n') : undefined;
}

// readHomeManual reads the seeded, operator-mergeable copy of ant/CLAUDE.md —
// the same file Claude Code discovers on its own, so every harness runs on
// one declaration (BUGS L130).
function readHomeManual(): string | null {
  try {
    return fs.readFileSync(`${HOME}/.claude/CLAUDE.md`, 'utf-8').trim() || null;
  } catch {
    return null;
  }
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

async function deliverTurn(turnID: string, output: ContainerOutput) {
  if (!turnID) {
    log(`deliverTurn skipped: no turn_id (status=${output.status})`);
    return;
  }
  try {
    return await submitTurn({
      turn_id: turnID,
      session_id: output.newSessionId,
      status: output.status,
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

// NUDGE_TEXT is the chassis-generated progress prompt. It goes straight onto the
// steer spool — routed through the IPC dir it was filtered out on the way to the
// spool, so every nudge was consumed and dropped. Between turns a stale nudge is
// dropped by exact text: it steers a RUNNING turn, never starts one.
export const NUDGE_TEXT =
  'Report progress to the user now. Use <status>short summary of what you are doing</status>.';

function nudgeProgress(): void {
  appendLines(STEER_SPOOL, [NUDGE_TEXT]);
  log('Nudged agent for progress report');
}

// STATUS_RE matches one <status>...</status> block. The agent emits these for
// interim progress; we forward each mid-turn so the user sees progress.
const STATUS_RE = /<status>([\s\S]*?)<\/status>/g;

// statusBlocks extracts the trimmed non-empty <status> blocks from one chunk of
// interim assistant text. THE one extraction site: every harness's status frame
// flows through here — before the seam inversion this ran on claude's raw
// message shape only, so codex streamed no interim status at all.
export function statusBlocks(text: string): string[] {
  const out: string[] = [];
  for (const m of text.matchAll(STATUS_RE)) {
    const s = m[1].trim();
    if (s) out.push(s);
  }
  return out;
}

// streamStatuses forwards <status> blocks to routd as interim notices. seen
// dedups across the turn so a status that also lands in the final result is not
// double-delivered. send is injected because this pipeline broke silently once
// already, so it needs a test that drives it.
async function streamStatuses(
  turnID: string,
  text: string,
  seen: Set<string>,
  send: typeof submitStatus = submitStatus,
): Promise<void> {
  if (!turnID) return;
  for (const s of statusBlocks(text)) {
    if (seen.has(s)) continue;
    const kind = seen.size === 0 ? 'summary' : 'progress';
    try {
      await send({ turn_id: turnID, text: s, kind });
      seen.add(s);
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
        if (data.source === 'self') continue;
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

// drainSteering empties the steer spool for a BETWEEN-turns read, dropping stale
// progress nudges.
function drainSteering(): string[] {
  return drainLines(STEER_SPOOL).filter(l => l !== NUDGE_TEXT);
}

// checkIpcMessage assembles the next inner-loop prompt from both queues — the
// IPC dir AND the steer spool. A turn that stopped calling tools would otherwise
// strand spooled steering here and the loop would exit "input empty".
export function checkIpcMessage(): string | null {
  if (shouldClose()) return null;
  const messages = [...drainSteering(), ...drainIpcInput()];
  return messages.length > 0 ? messages.join('\n') : null;
}

interface TurnResult {
  // token is the harness's latest continuity token, opaque to us.
  token?: string;
  // sessionId is the harness-native session id for routd's lineage row.
  sessionId?: string;
  closedDuringQuery: boolean;
  retryFresh: boolean;
  // failed says the turn produced no answer, and whether a retry could. The
  // container's exit code carries it to runed, which is what puts the turn on
  // routd's existing failure path instead of delivering the harness's error
  // line to the chat as the reply (BUGS L175).
  failed?: 'retryable' | 'terminal';
  // failedReason is the harness's own words for why, carried to the LAST line this
  // process writes. routd's journal entry is built from the stderr tail, so a reason
  // printed three lines earlier is outside it: 27 terminal turns on sloth said only
  // "Turn failed (terminal)" while the log on disk said "You've hit your individual
  // spend limit" (BUGS `L397`).
  failedReason?: string;
  nudge?: string;
}

// EXIT_TERMINAL is sysexits.h EX_CONFIG: "something was found in an
// unconfigured or misconfigured state". An exhausted quota or a spend cap is
// exactly that, and `container.terminalExit` reads this code as a fault a
// retry cannot repair.
const EXIT_TERMINAL = 78;

// runTurn drives one harness run and consumes its frames. retryFresh results are
// never delivered — the caller retries with no continuity. A retryFresh on a turn
// that was ALREADY fresh has nothing to retry, so it delivers as an error:
// suppressing it too was a silent-turn path.
async function runAttempt(
  harness: Harness,
  prompt: string,
  continuity: string | undefined,
  containerInput: ContainerInput,
  env: Record<string, string>,
  turnID: string,
  deliver: (turnID: string, output: ContainerOutput) => Promise<TurnAck | void> = deliverTurn,
  sendStatus: typeof submitStatus = submitStatus,
  deadline = PROCESS_DEADLINE,
): Promise<TurnResult> {
  const controller = new AbortController();
  const turn: Turn = {
    prompt,
    cwd: HOME,
    turnID,
    model: env.ARIZUKO_MODEL || undefined,
    effort: env.ARIZUKO_EFFORT || undefined,
    continuity,
    systemPrompt: buildSystemPrompt(containerInput, harness.profile),
    tools: injectMcpEnv(env),
    env,
    signal: controller.signal,
    deadline,
  };

  let ipcPolling = true;
  let closedDuringQuery = false;
  const pollIpcDuringQuery = () => {
    if (!ipcPolling) return;
    // The chassis owns the IPC mechanism; the `steering` policy owns delivery.
    // Queueing here rather than inside a hook is what lets codex reach it — its
    // hooks are separate processes and cannot be handed a closure.
    appendLines(STEER_SPOOL, drainIpcInput());
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

  // The nudge asks the agent for a <status> report. Wall-clock, so a long
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
        await streamStatuses(turnID, frame.text, seenStatuses, sendStatus);
      } else {
        resultCount++;
        log(`Result #${resultCount}: ok=${frame.ok}${frame.retryFresh ? ' retryFresh' : ''}${frame.text ? ` text=${frame.text.slice(0, 200)}` : ''}${frame.usage ? ` models=${Object.keys(frame.usage).join(',')}` : ''}`);
        if (frame.retryFresh && continuity !== undefined) {
          log('Session error, will retry without session');
          out.retryFresh = true;
        } else {
          if (frame.retryFresh) log('Session error on an already-fresh turn, delivering as error');
          // A failed turn's text is a diagnostic, never the answer. It rides
          // `error`, so routd delivers its own failure notice rather than
          // handing the user "API Error: 400 …" as the agent's reply.
          if (!frame.ok) {
            out.failed = frame.terminal ? 'terminal' : 'retryable';
            out.failedReason = frame.text;
          }
          const ack = await deliver(turnID, {
            status: frame.ok ? 'success' : 'error',
            newSessionId: out.sessionId,
            error: frame.ok
              ? undefined
              : frame.retryFresh
                ? 'harness could not complete the turn on a fresh session'
                : frame.text || 'the harness failed this turn',
            models: frame.usage,
            timedOut: frame.timedOut,
          });
          if (ack?.nudge) out.nudge = ack.nudge;
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

  // The seam's first requirement is exactly one terminal frame per turn, never
  // silence. Enforced here, at the one consumer, rather than trusting every
  // occupant.
  if (resultCount === 0 && !out.retryFresh && !closedDuringQuery) {
    log('Harness produced no result frame, delivering an error turn');
    out.failed = 'retryable';
    await deliver(turnID, {
      status: 'error',
      newSessionId: out.sessionId,
      error: 'harness produced no result for this turn',
    });
  }

  out.closedDuringQuery = closedDuringQuery;
  log(`Turn done. Frames: ${frameCount}, results: ${resultCount}, sessionId: ${out.sessionId || 'none'}, closedDuringQuery: ${closedDuringQuery}`);
  return out;
}

export async function runTurn(
  harness: Harness,
  prompt: string,
  continuity: string | undefined,
  containerInput: ContainerInput,
  env: Record<string, string>,
  turnID: string,
  deliver: (turnID: string, output: ContainerOutput) => Promise<TurnAck | void> = deliverTurn,
  sendStatus: typeof submitStatus = submitStatus,
  deadline = PROCESS_DEADLINE,
) {
  let sessionId: string | undefined;
  while (true) {
    const result = await runAttempt(harness, prompt, continuity, containerInput, env, turnID, deliver, sendStatus, deadline);
    continuity = result.token ?? continuity;
    sessionId = result.sessionId ?? sessionId;
    if (!result.nudge || result.failed || result.closedDuringQuery || result.retryFresh) {
      return { ...result, token: continuity, sessionId };
    }
    prompt = result.nudge;
  }
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
    // Both queues: what IPC holds now, plus anything the steering policy never
    // got to deliver because the last turn stopped calling tools.
    const pending = [...drainSteering(), ...drainIpcInput()];
    if (pending.length > 0) {
      log(`Draining ${pending.length} pending IPC messages into initial prompt`);
      prompt += '\n' + pending.join('\n');
    }

    // The platform hands back the sessionId a continuity frame carried; it is a
    // valid token for the harness that minted it. The harness unpacks and validates
    // — a placeholder or a pruned transcript starts fresh inside run().
    let continuity = containerInput.sessionId;
    let lastSessionId = containerInput.sessionId;
    const seedTurnID = containerInput.messageId || `boot-${Date.now()}`;
    let turnIndex = 0;
    try {
      // Selected inside the delivering try: an unknown ARIZUKO_BACKEND fails this turn
      // loudly to the user, never a silent exit.
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

        // The turn delivered no answer. Exit non-zero so runed reports the run
        // as failed and routd's own failure path runs — the one that retries,
        // marks the chat errored and tells the user. Staying alive for the next
        // message would hide a model outage behind a clean-looking container.
        if (r.failed) {
          // The reason comes BEFORE the explanation of the exit, because runed keeps
          // a TAIL: a reason at the end of the sentence is the first thing cut.
          log(
            `Turn failed (${r.failed})` +
              (r.failedReason ? `: ${r.failedReason}` : '') +
              '; exiting so the run is recorded as a failure',
          );
          process.exit(r.failed === 'terminal' ? EXIT_TERMINAL : 1);
        }

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
        newSessionId: lastSessionId,
        error: errorMessage,
      });
      process.exit(1);
    }
  } finally {
    cleanupSecrets();
  }
}

// Run main only as the process entrypoint, so tests can import the exported
// helpers without starting the stdin-reading runtime.
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
