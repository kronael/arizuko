// Codex backend — drives `codex app-server` (JSON-RPC 2.0 over stdio). Second
// implementation of the Backend interface (spec 5/K). Spawn the subprocess,
// start (or resume) a thread, run a turn, stream normalized events, finish on
// turn/finished. MCP servers render into codex's native config so tool calls
// flow through the same gated socket.
//
// v1 skill handling is system-prompt concatenation (the lossy-but-simple
// path, spec 5/K §"Folder shape compatibility" option 1): the systemPrompt
// the runtime built is passed straight through as the turn's instructions.
//
// Not live-smoke-tested here — codex auth is absent in this worktree. The
// JSON-RPC mapping table (spec 5/K) is implemented; structural completeness
// is the gate.

import { spawn, ChildProcessWithoutNullStreams } from 'child_process';
import { Backend, Caps, Event, Session, SessionConfig } from './types.js';

function codexBin(): string {
  return process.env.CODEX_BIN || 'codex';
}

function log(message: string): void {
  console.error(`[ant] ${message}`);
}

interface JsonRpcResponse {
  jsonrpc: '2.0';
  id?: number | string;
  result?: unknown;
  error?: { code: number; message: string };
  method?: string;     // notifications carry method, no id
  params?: unknown;
}

// systemPromptText flattens the SessionConfig systemPrompt union to a string.
// The preset form has no codex analog; an empty string lets codex use its own
// default instructions.
function systemPromptText(sp: SessionConfig['systemPrompt']): string {
  if (typeof sp === 'string') return sp;
  return '';
}

// renderMcpConfig maps the assembled MCP server map into codex's app-server
// config shape (mcp_servers: name → {command,args,env}). Both harnesses speak
// MCP natively, so no schema translation is needed (spec 5/K §"Tool-use
// bridging"). The gated socat server rides through unchanged.
function renderMcpConfig(
  servers: Record<string, import('../mcp-servers.js').McpServerConfig>,
): Record<string, { command: string; args: string[]; env: Record<string, string> }> {
  const out: Record<string, { command: string; args: string[]; env: Record<string, string> }> = {};
  for (const [name, cfg] of Object.entries(servers)) {
    out[name] = {
      command: cfg.command,
      args: cfg.args ?? [],
      env: cfg.env ?? {},
    };
  }
  return out;
}

// CodexSession owns one codex app-server subprocess and one thread within it.
// JSON-RPC requests are line-framed over stdin; responses and event
// notifications arrive line-framed over stdout. Events normalize per the 5/K
// mapping table and feed the same channel the runtime already reads.
class CodexSession implements Session {
  private proc: ChildProcessWithoutNullStreams;
  private nextId = 1;
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
  private stdoutBuf = '';
  private threadId: string | null = null;
  private turnId: string | null = null;
  private closed = false;

  // Event delivery: a single-consumer async queue. Notifications push
  // normalized events; the result event ends the stream.
  private evQueue: Event[] = [];
  private evWaiting: (() => void) | null = null;
  private evDone = false;

  constructor(private cfg: SessionConfig) {
    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(cfg.env ?? {})) {
      if (v !== undefined) env[k] = v;
    }
    this.proc = spawn(codexBin(), ['app-server'], {
      cwd: cfg.cwd,
      env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.proc.stdout.setEncoding('utf8');
    this.proc.stdout.on('data', (chunk: string) => this.onStdout(chunk));
    this.proc.stderr.setEncoding('utf8');
    this.proc.stderr.on('data', (chunk: string) => log(`codex stderr: ${chunk.trimEnd()}`));
    this.proc.on('exit', (code) => {
      log(`codex app-server exited (code=${code})`);
      this.failPending(new Error(`codex app-server exited (code=${code})`));
      this.endEvents();
    });
    this.proc.on('error', (err) => {
      log(`codex spawn error: ${err.message}`);
      this.failPending(err);
      this.endEvents();
    });
  }

  // request sends a JSON-RPC call and resolves on the matching response.
  private request(method: string, params: unknown): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const id = this.nextId++;
      this.pending.set(id, { resolve, reject });
      const frame = JSON.stringify({ jsonrpc: '2.0', id, method, params });
      try {
        this.proc.stdin.write(frame + '\n');
      } catch (err) {
        this.pending.delete(id);
        reject(err instanceof Error ? err : new Error(String(err)));
      }
    });
  }

  // notify sends a fire-and-forget JSON-RPC notification (no id, no response).
  private notify(method: string, params: unknown): void {
    const frame = JSON.stringify({ jsonrpc: '2.0', method, params });
    try {
      this.proc.stdin.write(frame + '\n');
    } catch (err) {
      log(`codex notify failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  private onStdout(chunk: string): void {
    this.stdoutBuf += chunk;
    let nl: number;
    while ((nl = this.stdoutBuf.indexOf('\n')) >= 0) {
      const line = this.stdoutBuf.slice(0, nl);
      this.stdoutBuf = this.stdoutBuf.slice(nl + 1);
      if (!line.trim()) continue;
      let msg: JsonRpcResponse;
      try {
        msg = JSON.parse(line) as JsonRpcResponse;
      } catch {
        log(`codex: dropping non-JSON stdout line`);
        continue;
      }
      this.dispatch(msg);
    }
  }

  private dispatch(msg: JsonRpcResponse): void {
    // Response to one of our requests.
    if (msg.id !== undefined && (msg.result !== undefined || msg.error !== undefined)) {
      const id = typeof msg.id === 'number' ? msg.id : Number(msg.id);
      const p = this.pending.get(id);
      if (p) {
        this.pending.delete(id);
        if (msg.error) p.reject(new Error(`codex rpc error: ${msg.error.message}`));
        else p.resolve(msg.result);
      }
      return;
    }
    // Event notification.
    if (msg.method) {
      const ev = this.normalize(msg.method, msg.params);
      if (ev) this.pushEvent(ev);
    }
  }

  // normalize maps a codex notification onto a Backend Event. Mapping table
  // from spec 5/K §"Event normalization".
  private normalize(method: string, params: unknown): Event | null {
    const raw = (params && typeof params === 'object' ? params : {}) as Record<string, unknown>;
    switch (method) {
      case 'thread/started': {
        const threadId = typeof raw.threadId === 'string' ? raw.threadId : undefined;
        if (threadId) this.threadId = threadId;
        return { type: 'system_init', raw, sessionId: threadId };
      }
      case 'item/agentMessage/delta':
      case 'item/agentMessage/done': {
        const text = typeof raw.text === 'string' ? raw.text
          : typeof raw.delta === 'string' ? raw.delta : undefined;
        return { type: 'assistant', raw, text };
      }
      case 'item/mcpToolCall':
      case 'command/exec':
        return { type: 'tool_use', raw };
      case 'item/toolResult':
        return { type: 'tool_result', raw };
      case 'turn/finished': {
        const status: 'success' | 'error' = raw.error ? 'error' : 'success';
        const text = typeof raw.text === 'string' ? raw.text : undefined;
        return { type: 'result', raw, text, final: true, status, models: extractCodexUsage(raw) };
      }
      default:
        return null;
    }
  }

  private pushEvent(ev: Event): void {
    this.evQueue.push(ev);
    this.evWaiting?.();
    if (ev.final) this.endEvents();
  }

  private endEvents(): void {
    this.evDone = true;
    this.evWaiting?.();
  }

  private failPending(err: Error): void {
    for (const p of this.pending.values()) p.reject(err);
    this.pending.clear();
  }

  // start brings the session up: thread/start (resume if a thread id is
  // given) then turn/start with the initial prompt. Called once by events().
  private async start(): Promise<void> {
    const mcpServers = renderMcpConfig(this.cfg.mcpServers ?? {});
    const instructions = systemPromptText(this.cfg.systemPrompt);
    const startParams: Record<string, unknown> = {
      cwd: this.cfg.cwd,
      mcpServers,
      instructions,
    };
    if (this.cfg.model) startParams.model = this.cfg.model;
    if (this.cfg.resume) startParams.resume = this.cfg.resume;

    const started = await this.request('thread/start', startParams) as { threadId?: string };
    if (started && typeof started.threadId === 'string') this.threadId = started.threadId;

    const turn = await this.request('turn/start', {
      threadId: this.threadId,
      input: this.cfg.prompt,
    }) as { turnId?: string };
    if (turn && typeof turn.turnId === 'string') this.turnId = turn.turnId;
  }

  sendUserMessage(text: string): void {
    // Mid-turn steering maps to turn/steer; a fresh turn maps to turn/start.
    if (this.turnId) {
      this.notify('turn/steer', { threadId: this.threadId, turnId: this.turnId, input: text });
    } else {
      this.request('turn/start', { threadId: this.threadId, input: text })
        .then((t) => { const tt = t as { turnId?: string }; if (tt?.turnId) this.turnId = tt.turnId; })
        .catch((err) => log(`codex turn/start (steer) failed: ${err.message}`));
    }
  }

  interrupt(): void {
    if (!this.threadId) return;
    this.notify('turn/interrupt', { threadId: this.threadId, turnId: this.turnId });
  }

  setModel(model: string): void {
    this.notify('thread/setModel', { threadId: this.threadId, model });
  }

  setPermissionMode(mode: string): void {
    this.notify('thread/setPermissionMode', { threadId: this.threadId, mode });
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    try {
      if (this.threadId) await this.request('thread/close', { threadId: this.threadId });
    } catch (err) {
      log(`codex thread/close failed: ${err instanceof Error ? err.message : String(err)}`);
    }
    try { this.proc.stdin.end(); } catch { /* ignore */ }
    try { this.proc.kill(); } catch { /* ignore */ }
    this.endEvents();
  }

  async *events(): AsyncIterable<Event> {
    await this.start();
    while (true) {
      while (this.evQueue.length > 0) {
        yield this.evQueue.shift()!;
      }
      if (this.evDone) return;
      await new Promise<void>(r => { this.evWaiting = r; });
      this.evWaiting = null;
    }
  }
}

// extractCodexUsage pulls per-model token/cost accounting from a turn/finished
// payload when present. Codex's usage shape is younger than claude's; absent
// fields no-op (gated tolerates a missing models map). Spec 5/K open question 5.
function extractCodexUsage(raw: Record<string, unknown>): Record<string, import('../mcp.js').ModelUsage> | undefined {
  const usage = raw.usage;
  if (!usage || typeof usage !== 'object') return undefined;
  const u = usage as {
    model?: string;
    inputTokens?: number;
    outputTokens?: number;
    cachedInputTokens?: number;
    costUSD?: number;
  };
  const model = u.model || 'codex';
  return {
    [model]: {
      input: u.inputTokens ?? 0,
      output: u.outputTokens ?? 0,
      cache_read: u.cachedInputTokens ?? 0,
      cache_write: 0,
      cost_cents: Math.round((u.costUSD ?? 0) * 100),
    },
  };
}

export class CodexBackend implements Backend {
  name(): string {
    return 'codex';
  }

  capabilities(): Caps {
    return {
      streaming: true,
      interrupt: true,
      multiTurn: true,
      setModelLive: true,
      permissionPrompt: true,
      toolUse: true,
      sessionResume: true,
      mcpClient: true,
    };
  }

  async spawn(cfg: SessionConfig): Promise<Session> {
    return new CodexSession(cfg);
  }
}
