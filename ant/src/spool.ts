// spool.ts — the two line-queues that cross codex's hook process boundary.
//
// codex runs each hook as its own process. It cannot be handed a closure, and
// it SWALLOWS the hook's stderr (measured 2026-08-21 — a marker written to fd 2
// reaches neither codex's stdout nor its stderr). So both directions travel
// through a file: the chassis queues STEERING in, the hook queues LOG LINES
// back out. One format, two queues, no other state.
//
// Paths are compiled constants, never env vars: a var ant reads that nothing
// emits as `-e` is silently dead, and that class already cost ARIZUKO_MODEL,
// ARIZUKO_BACKEND and ARIZUKO_IS_ROOT (BUGS L60). /tmp is container-private and
// per-spawn, so there is nothing to collide with.

import fs from 'fs';

export const STEER_FILE = '/tmp/arizuko-steer.jl';
export const POLICY_LOG_FILE = '/tmp/arizuko-policy.jl';

// encodeLines is THE on-disk format: one JSON string per line, so a message
// containing a newline survives the round trip.
function encodeLines(lines: string[]): string {
  return lines.map((l) => JSON.stringify(l)).join('\n') + '\n';
}

export function appendLines(file: string, lines: string[]): void {
  if (lines.length === 0) return;
  try {
    fs.appendFileSync(file, encodeLines(lines));
  } catch (err) {
    console.error(`[ant] spool: append to ${file} failed: ${err}`);
  }
}

// drainLines reads a queue and empties it in one step. Truncate rather than
// unlink: an empty file is the same "nothing queued" state as an absent one,
// and the reader may not own the directory entry.
export function drainLines(file: string): string[] {
  let raw: string;
  try {
    raw = fs.readFileSync(file, 'utf-8');
  } catch {
    return []; // nothing was ever queued
  }
  if (raw === '') return [];
  try {
    fs.truncateSync(file, 0);
  } catch (err) {
    // A queue we cannot empty replays its whole contents on every drain.
    console.error(`[ant] spool: truncate of ${file} failed, dropping the batch: ${err}`);
    return [];
  }
  const out: string[] = [];
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    try {
      out.push(String(JSON.parse(line)));
    } catch {
      out.push(line);
    }
  }
  return out;
}

// spoolStderr redirects console.error for the life of this process. Called only
// by policy-cli.ts — an in-process harness must keep the real stderr.
//
// It writes the file directly rather than through appendLines: appendLines
// reports its own failures on console.error, which this function has just
// replaced, so the pair would recurse until the stack ran out.
export function spoolStderr(): void {
  const real = console.error.bind(console);
  console.error = (...args: unknown[]) => {
    const line = args.map((a) => (typeof a === 'string' ? a : String(a))).join(' ');
    try {
      fs.appendFileSync(POLICY_LOG_FILE, encodeLines([line]));
    } catch (err) {
      real(`[ant] spool: ${err}`);
    }
  };
}
