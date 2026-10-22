// policy-cli.ts — the codex renderer's other half. codex registers a hook as a
// COMMAND, so a policy living in ant's process is unreachable from it. codex
// runs `node /tmp/dist/policy-cli.js <event> <turn-id>`, hands the event on
// stdin, and reads the merged verdict on stdout. One process per EVENT, not per
// row.
//
// It must never fail loudly: a non-zero exit or malformed stdout makes codex
// treat the tool call as blocked, so a bug in a log line would refuse the
// agent's own tools. Every path prints something valid.

import os from 'os';
import { PolicyEvent, ToolEvent, bindings, runRows } from './policy.js';
import { codexRows, verdictToCodexJson } from './codex.js';
import { spoolStderr } from './spool.js';

function readStdin(): Promise<string> {
  return new Promise((resolve) => {
    let raw = '';
    process.stdin.setEncoding('utf-8');
    process.stdin.on('data', (chunk) => { raw += chunk; });
    process.stdin.on('end', () => resolve(raw));
    process.stdin.on('error', () => resolve(raw));
  });
}

async function main(): Promise<void> {
  spoolStderr();
  const [event, turnID] = process.argv.slice(2);
  const ev = JSON.parse(await readStdin()) as ToolEvent;
  const rows = codexRows(bindings({ home: os.homedir(), turnID: turnID ?? '' }));
  const verdict = await runRows(rows, event as PolicyEvent, ev);
  process.stdout.write(verdictToCodexJson(verdict, event as PolicyEvent));
}

// No process.exit: it can truncate a stdout write still in flight, and codex
// reads that write to decide whether the tool call proceeds.
main().catch((err) => {
  console.error(`[ant] policy-cli: ${err instanceof Error ? err.message : String(err)}`);
  process.stdout.write('{}');
});
