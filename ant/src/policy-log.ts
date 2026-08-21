// policy-log.ts — where a policy's log lines go when nobody is listening.
//
// codex runs each hook as its own process and SWALLOWS its stderr: a marker
// written to fd 2 there appears in neither codex's stdout nor its stderr
// (measured 2026-08-21). Every policy already logs through console.error — the
// tool log IS console.error — so the CLI redirects that one function into a
// spool file, and the codex harness drains it into its own stderr as frames
// arrive. Nothing else changes; claude and pi run in-process and keep fd 2.

import fs from 'fs';
import { encodeLines } from './spool.js';

export const POLICY_LOG_FILE = '/tmp/arizuko-policy.jl';

// spoolStderr redirects console.error for the life of this process. Called only
// by policy-cli.ts — in-process harnesses must keep the real stderr.
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
      real(`[ant] policy-log: ${err}`);
    }
  };
}
