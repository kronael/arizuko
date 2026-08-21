// policy-cli.ts — the codex renderer's other half.
//
// codex registers a hook as a COMMAND, so a policy that lives in ant's process
// is unreachable from it. This is the bridge: codex runs
// `node /tmp/dist/policy-cli.js <binding-id> <turn-id>`, hands the event on
// stdin, and reads the verdict on stdout. The binding is the same row claude
// renders as an SDK callback — one implementation, two ways in.
//
// It must never fail loudly: a non-zero exit or malformed stdout makes codex
// treat the call as blocked, so a bug in a log line would refuse the agent's
// tools. Every path prints something valid and exits 0.

import os from 'os';
import { ToolEvent, findBinding } from './policy.js';
import { verdictToCodexJson } from './codex.js';
import { spoolStderr } from './policy-log.js';

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
  const [id, turnID] = process.argv.slice(2);
  const raw = await readStdin();
  const event = JSON.parse(raw) as ToolEvent;
  const binding = findBinding({ home: os.homedir(), turnID: turnID ?? '' }, id);
  if (!binding) {
    console.error(`[ant] policy-cli: no binding named ${id}`);
    return;
  }
  process.stdout.write(verdictToCodexJson(await binding.run(event), binding.event));
}

// No process.exit: it can truncate a stdout write still in flight, and codex
// reads that write to decide whether the tool call proceeds. The process ends
// on its own once stdin is closed.
main().catch((err) => {
  console.error(`[ant] policy-cli: ${err instanceof Error ? err.message : String(err)}`);
  process.stdout.write('{}');
});
