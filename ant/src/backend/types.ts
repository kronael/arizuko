// Backend abstraction — ant wraps agentic harnesses, never is one. Spec 5/K.
//
// A Backend spawns one harness subprocess per session and drives it over the
// harness's wire protocol. The MCP surface above (submit_turn, the arizuko
// socket, the agent's tools) is identical regardless of which harness runs.

// EventType is the union of categories all harnesses share. Spec 5/K
// "Event normalization".
export type EventType =
  | 'system_init'   // session ready (claude system/init, codex thread/started)
  | 'assistant'     // assistant text (delta or done)
  | 'tool_use'      // a tool was invoked
  | 'tool_result'   // a tool returned
  | 'result'        // turn-terminating result (claude result, codex turn/finished)
  | 'rate_limit'    // harness reported a rate-limit
  | 'keep_alive';   // heartbeat

// Event is a normalized event from any harness. `raw` preserves the
// harness-native payload verbatim so callers that want full fidelity
// (thinking blocks, tool-use shapes, usage stats) get everything.
export interface Event {
  type: EventType;
  raw: Record<string, unknown>; // harness-native payload, verbatim
  text?: string;                // best-effort extracted assistant text
  final?: boolean;              // true on the turn-terminating event
  // sessionId is set on system_init events: the harness's session/thread id.
  sessionId?: string;
  // status is set on result events: how the turn ended.
  status?: 'success' | 'error';
  // models is per-model token/cost accounting, set on result events when the
  // harness reports it. Snake_case to match the submit_turn payload (5/34).
  models?: Record<string, import('../mcp.js').ModelUsage>;
  // timedOut is set on a result the backend synthesised after the query-timeout
  // deadline (graceful summary, not a normal completion). routd logs a WARN.
  timedOut?: boolean;
}

// SessionConfig is the union of what any backend might accept. Backends ignore
// fields they don't support; caps() declares which are honored. Spec 5/K.
export interface SessionConfig {
  prompt: string;                 // the initial user turn
  model?: string;
  cwd?: string;
  resume?: string;                // session/thread id to resume
  resumeAt?: string;              // resume at a specific point (claude resumeSessionAt)
  systemPrompt?: string | { type: 'preset'; preset: 'claude_code' };
  permissionMode?: string;
  addDirs?: string[];
  env?: Record<string, string | undefined>;
  // mcpServers is the assembled MCP server map (injectMcpEnv output). The
  // backend renders it into the harness's native MCP-client config.
  mcpServers?: Record<string, import('../mcp-servers.js').McpServerConfig>;
  // assistantName threads through to claude's PreCompact archival hook.
  assistantName?: string;
}

// Caps is what a backend reports up front. The MCP layer can surface this so
// callers don't ask for things the backend can't do. Spec 5/K.
export interface Caps {
  streaming: boolean;
  interrupt: boolean;
  multiTurn: boolean;
  setModelLive: boolean;
  permissionPrompt: boolean;
  toolUse: boolean;
  sessionResume: boolean;
  mcpClient: boolean;
}

// Session drives one live harness session over its wire protocol.
export interface Session {
  // events streams normalized output until the turn terminates. The result
  // event carries final:true; iteration then ends.
  events(): AsyncIterable<Event>;
  // sendUserMessage steers a new user turn into the live session.
  sendUserMessage(text: string): void;
  // interrupt requests a mid-turn stop.
  interrupt(): void;
  // setModel switches the model live (no-op if !caps.setModelLive).
  setModel(model: string): void;
  // setPermissionMode switches permission mode live.
  setPermissionMode(mode: string): void;
  // close tears the session down.
  close(): Promise<void>;
}

// Backend spawns and owns harness sessions. One Backend per ant process;
// it produces a fresh Session per agent run.
export interface Backend {
  name(): string;             // "claude" | "codex" | …
  capabilities(): Caps;
  spawn(cfg: SessionConfig): Promise<Session>;
}
