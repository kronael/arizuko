// backend.ts — the connector seam (spec 6/7 § "The connector seam"). The
// contract stays the process boundary: envelope on stdin, tools on the gated
// MCP socket, submit_turn for every frame — anything speaking that replaces
// the whole occupant. This file is the chassis-side seam so a CLI harness
// reuses ant's runtime half (index.ts: stdin parse, IPC steering, idempotent
// submit_turn) instead of reimplementing it: one interface, one file per
// backend (claude.ts, codex.ts, pi.ts).

import type { Readable } from 'stream';
import type { ModelUsage } from './mcp.js';
import type { McpServerConfig } from './mcp-servers.js';
import { claudeBackend } from './claude.js';
import { codexBackend } from './codex.js';
import { piBackend } from './pi.js';

// EventType is the category index.ts switches on. One per normalized message
// shape — nothing else reaches the runtime.
export type EventType =
  | 'system_init'   // session ready
  | 'assistant'     // assistant text
  | 'tool_result'   // a tool returned
  | 'result'        // turn-terminating result
  | 'rate_limit';   // the harness reported a rate-limit

// Event is one normalized harness message. `raw` preserves the harness payload
// verbatim so callers keep full fidelity.
export interface Event {
  type: EventType;
  raw: Record<string, unknown>; // harness payload, verbatim
  text?: string;                // best-effort extracted assistant text
  final?: boolean;              // true on the turn-terminating event
  // sessionId is set on system_init events: the harness's session id.
  sessionId?: string;
  // status is set on result events: how the turn ended.
  status?: 'success' | 'error';
  // models is per-model token/cost accounting, set on result events when the
  // harness reports it. Snake_case to match the submit_turn payload (spec 11/19).
  models?: Record<string, ModelUsage>;
  // timedOut marks a result synthesised after the query-timeout deadline
  // (graceful summary, not a normal completion). routd logs a WARN.
  timedOut?: boolean;
}

// SessionConfig is what index.ts hands Backend.spawn for one turn. Chassis
// facts; each backend uses what its harness can express and ignores the rest.
export interface SessionConfig {
  prompt: string;                 // the initial user turn
  model?: string;                 // backend-native model id, verbatim
  cwd?: string;
  resume?: string;                // session id to resume (claude)
  resumeAt?: string;              // resume at a specific point (claude)
  systemPrompt?: string | { type: 'preset'; preset: 'claude_code' };
  addDirs?: string[];
  env?: Record<string, string | undefined>;
  // mcpServers is the assembled MCP server map (injectMcpEnv output — the
  // single declaration). claude renders it into SDK options; codex renders it
  // into -c mcp_servers.* overrides; pi has no MCP client and cannot read it.
  mcpServers?: Record<string, McpServerConfig>;
  // assistantName threads through to the PreCompact archival hook (claude).
  assistantName?: string;
  // turnID is the arizuko turn id, threaded to the TodoWrite status hook so it
  // keys the live-edited ⏳ message (spec 5/24). Empty → the hook no-ops.
  turnID?: string;
}

// DrainIpcInput is provided by the runtime so a backend that can steer
// mid-turn pulls queued user messages into the active query. Backends that
// cannot steer ignore it; the runtime's between-turn loop still drains.
export type DrainIpcInput = () => string[];

// Session is one running turn.
export interface Session {
  events(): AsyncIterable<Event>;
  close(): Promise<void>;
}

// Backend is the seam: the harness half of one occupant. resolveResume and
// isSessionError keep harness-specific session semantics out of index.ts.
export interface Backend {
  name: string;
  // resolveResume maps a stored session id to what this backend can resume;
  // undefined = start fresh. Layer-1 policy is the backend's own (spec 6/7:
  // a conformant backend works with layer 1 empty).
  resolveResume(sessionId: string | undefined): string | undefined;
  // isSessionError reports a result subtype meaning the resume target was
  // unusable — a retry-fresh signal, never delivered as a reply.
  isSessionError(subtype: string | undefined): boolean;
  spawn(cfg: SessionConfig, drain: DrainIpcInput): Session;
}

// selectBackend maps ARIZUKO_BACKEND to an occupant. Unset defaults to
// claude; an unknown value throws so a misconfigured spawn fails loudly to
// the user instead of silently running the wrong harness.
export function selectBackend(name: string | undefined): Backend {
  switch (name || 'claude') {
    case 'claude': return claudeBackend;
    case 'codex': return codexBackend;
    case 'pi': return piBackend;
    default:
      throw new Error(`unknown ARIZUKO_BACKEND ${JSON.stringify(name)} (claude|codex|pi)`);
  }
}

// jsonLines yields one parsed JSON object per stdout line of a JSONL-emitting
// CLI. Unparseable lines are skipped (harness banners, partial flushes) — the
// per-backend state machine decides what a missing terminal means.
export async function* jsonLines(stream: Readable): AsyncGenerator<Record<string, unknown>> {
  let buf = '';
  stream.setEncoding('utf8');
  for await (const chunk of stream) {
    buf += chunk;
    let nl: number;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      try {
        yield JSON.parse(line) as Record<string, unknown>;
      } catch { /* not JSON — skip */ }
    }
  }
  const tail = buf.trim();
  if (tail) {
    try {
      yield JSON.parse(tail) as Record<string, unknown>;
    } catch { /* not JSON — skip */ }
  }
}
