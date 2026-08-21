// spool.ts — the two line-queues that cross codex's hook process boundary.
//
// codex runs each hook as its own process. It cannot be handed a closure, and
// it SWALLOWS the hook's stderr (measured 2026-08-21 — a marker written to fd 2
// reaches neither codex's stdout nor its stderr). So both directions travel
// through the filesystem: the chassis queues STEERING in, the hook queues LOG
// LINES back out. One format, two queues, no other state.
//
// A queue is a DIRECTORY of batch files, not one appended file. The single-file
// shape lost messages by construction: the reader's read→truncate pair raced a
// concurrent writer's append (a steering line landing between the two syscalls
// was truncated away, unread), and two concurrent readers both saw the same
// content before either truncated (double delivery). Per-batch files close
// both: a write is publish-by-rename (a batch is never visible half-written),
// and a drain CLAIMS each batch by renaming it aside — rename is atomic, so
// exactly one drainer wins each batch.
//
// Paths are compiled constants, never env vars: a var ant reads that nothing
// emits as `-e` is silently dead, and that class already cost ARIZUKO_MODEL,
// ARIZUKO_BACKEND and ARIZUKO_IS_ROOT (BUGS L60). /tmp is container-private and
// per-spawn, so there is nothing to collide with.

import fs from 'fs';
import path from 'path';

export const STEER_SPOOL = '/tmp/arizuko-steer';
export const POLICY_LOG_SPOOL = '/tmp/arizuko-policy';

// encodeLines is THE batch format: one JSON string per line, so a message
// containing a newline survives the round trip.
function encodeLines(lines: string[]): string {
  return lines.map((l) => JSON.stringify(l)).join('\n') + '\n';
}

// seq disambiguates same-millisecond batches from one process; the pid in the
// name disambiguates processes. Batch names sort in enqueue order because
// Date.now() is fixed-width for the next two centuries.
let seq = 0;

// writeBatch publishes one batch: write to a dot-prefixed temp name, then
// rename into place. A drainer only considers `*.jl` names, so a batch is
// either absent or whole — never a truncated read.
function writeBatch(dir: string, lines: string[]): void {
  fs.mkdirSync(dir, { recursive: true });
  const name = `${Date.now()}-${process.pid}-${String(seq++).padStart(6, '0')}`;
  const tmp = path.join(dir, `.${name}.tmp`);
  fs.writeFileSync(tmp, encodeLines(lines));
  fs.renameSync(tmp, path.join(dir, `${name}.jl`));
}

export function appendLines(dir: string, lines: string[]): void {
  if (lines.length === 0) return;
  try {
    writeBatch(dir, lines);
  } catch (err) {
    console.error(`[ant] spool: append to ${dir} failed: ${err}`);
  }
}

// drainLines empties the queue: claim each batch by renaming it to a
// dot-prefixed name (atomic — a concurrent drainer loses the rename and skips),
// read it, delete it. A batch that cannot be read after a successful claim is
// dropped loudly rather than replayed — same rule the old truncate failure had.
export function drainLines(dir: string): string[] {
  let names: string[];
  try {
    names = fs.readdirSync(dir);
  } catch {
    return []; // no queue directory → nothing was ever queued
  }
  const out: string[] = [];
  for (const name of names.filter((n) => n.endsWith('.jl')).sort()) {
    const claimed = path.join(dir, `.${name}.${process.pid}.claim`);
    try {
      fs.renameSync(path.join(dir, name), claimed);
    } catch {
      continue; // another drainer claimed it first
    }
    let raw = '';
    try {
      raw = fs.readFileSync(claimed, 'utf-8');
    } catch (err) {
      console.error(`[ant] spool: read of claimed ${name} failed, dropping the batch: ${err}`);
    }
    try {
      fs.unlinkSync(claimed);
    } catch (err) {
      console.error(`[ant] spool: unlink of claimed ${name} failed: ${err}`);
    }
    for (const line of raw.split('\n')) {
      if (!line.trim()) continue;
      try {
        out.push(String(JSON.parse(line)));
      } catch {
        out.push(line);
      }
    }
  }
  return out;
}

// spoolStderr redirects console.error for the life of this process. Called only
// by policy-cli.ts — an in-process harness must keep the real stderr.
//
// It writes batches directly rather than through appendLines: appendLines
// reports its own failures on console.error, which this function has just
// replaced, so the pair would recurse until the stack ran out.
export function spoolStderr(): void {
  const real = console.error.bind(console);
  console.error = (...args: unknown[]) => {
    const line = args.map((a) => (typeof a === 'string' ? a : String(a))).join(' ');
    try {
      writeBatch(POLICY_LOG_SPOOL, [line]);
    } catch (err) {
      real(`[ant] spool: ${err}`);
    }
  };
}
