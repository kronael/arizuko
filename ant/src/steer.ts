// steer.ts — the mid-turn steering queue.
//
// Steering used to be a closure passed through the harness seam and used by
// exactly one occupant. A closure cannot cross into codex's hook subprocess, so
// the queue became a file both sides reach (spool.ts): the chassis appends, the
// `steering` binding drains.
//
// The path is a compiled constant, never an env var — a var ant reads that
// nothing emits as `-e` is silently dead (BUGS L60 and its two predecessors).
// /tmp is container-private and per-spawn, so there is nothing to collide with.

import { ALLOW, Verdict } from './policy.js';
import { appendLines, drainLines } from './spool.js';

export const STEER_FILE = '/tmp/arizuko-steer.jl';

export function appendSteer(messages: string[]): void {
  appendLines(STEER_FILE, messages);
}

// drainSteerMessages empties the queue. The chassis calls it once more after
// the turn ends: a message queued while the agent never ran another tool would
// otherwise sit in the file, and it belongs at the head of the next prompt.
export function drainSteerMessages(): string[] {
  return drainLines(STEER_FILE);
}

// drainSteer is the `steering` policy: whatever the chassis queued since the
// last tool call, injected as context after this one.
export function drainSteer(): Verdict {
  const messages = drainSteerMessages();
  if (messages.length === 0) return ALLOW;
  return { kind: 'inject', context: `<user-steering>\n${messages.join('\n')}\n</user-steering>` };
}
