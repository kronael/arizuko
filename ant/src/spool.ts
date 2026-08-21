// spool.ts — a line queue in a file, for the two places a closure cannot reach.
//
// codex runs each hook as its own process: it cannot be handed a callback, and
// it SWALLOWS the hook's stderr (measured 2026-08-21 — a marker written to fd 2
// appears in neither codex's stdout nor its stderr). So both directions across
// that boundary travel through a file: the chassis queues steering INTO the
// hook, the hook queues log lines back OUT.
//
// One primitive, two users — never two half-identical file helpers.

import fs from 'fs';

// encodeLines is THE on-disk format: one JSON string per line, so a message
// containing a newline survives the round trip. Exported because policy-log.ts
// writes the same format without going through appendLines — it must not call
// console.error, which it has itself replaced.
export function encodeLines(lines: string[]): string {
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

// drainLines reads the queue and empties it in one step. Truncate rather than
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
