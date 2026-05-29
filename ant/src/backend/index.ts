// Backend selection. ARIZUKO_BACKEND picks the harness; default "claude".
// An unknown value is fatal at startup — no silent fallback (spec 5/K,
// CLAUDE.md "Strict, not magical").

import { Backend } from './types.js';
import { ClaudeBackend, DrainIpcInput } from './claude.js';
import { CodexBackend } from './codex.js';

export type { Backend, Session, Event, EventType, Caps, SessionConfig } from './types.js';
export type { DrainIpcInput } from './claude.js';
export { ClaudeBackend, renderMcpServers } from './claude.js';
export { CodexBackend } from './codex.js';

// selectBackend resolves ARIZUKO_BACKEND to a Backend instance. drain is the
// runtime's IPC-input reader, needed only by the claude backend's steering
// hook; codex ignores it.
export function selectBackend(name: string | undefined, drain: DrainIpcInput): Backend {
  const choice = (name || 'claude').trim();
  switch (choice) {
    case 'claude':
      return new ClaudeBackend(drain);
    case 'codex':
      return new CodexBackend();
    default:
      throw new Error(`unknown ARIZUKO_BACKEND "${choice}" (expected "claude" or "codex")`);
  }
}
