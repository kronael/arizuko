import { test } from 'node:test';
import assert from 'node:assert/strict';

const OUTPUT_START_MARKER = '---NANOCLAW_OUTPUT_START---';
const OUTPUT_END_MARKER = '---NANOCLAW_OUTPUT_END---';

// Capture stdout writes during fn, return accumulated string.
function captureStdout(fn: () => void): string {
  const chunks: string[] = [];
  const orig = process.stdout.write.bind(process.stdout);
  process.stdout.write = (chunk: string | Uint8Array) => {
    chunks.push(typeof chunk === 'string' ? chunk : chunk.toString());
    return true;
  };
  try {
    fn();
  } finally {
    process.stdout.write = orig;
  }
  return chunks.join('');
}

function writeHeartbeat(): void {
  process.stdout.write(OUTPUT_START_MARKER + '\n');
  process.stdout.write(JSON.stringify({ heartbeat: true }) + '\n');
  process.stdout.write(OUTPUT_END_MARKER + '\n');
}

test('writeHeartbeat emits markers and heartbeat JSON', () => {
  const out = captureStdout(writeHeartbeat);
  assert.ok(out.includes(OUTPUT_START_MARKER), 'missing start marker');
  assert.ok(out.includes(OUTPUT_END_MARKER), 'missing end marker');
  assert.ok(out.includes('"heartbeat":true'), 'missing heartbeat field');
  const lines = out.split('\n').filter(Boolean);
  assert.equal(lines[0], OUTPUT_START_MARKER);
  assert.equal(lines[2], OUTPUT_END_MARKER);
  const parsed = JSON.parse(lines[1]);
  assert.equal(parsed.heartbeat, true);
  assert.equal(parsed.status, undefined, 'heartbeat must not have status');
  assert.equal(parsed.result, undefined, 'heartbeat must not have result');
});

test('heartbeat interval fires at configured interval', async () => {
  let count = 0;
  const intervalMs = 20;
  const id = setInterval(() => { count++; }, intervalMs);
  await new Promise<void>(r => setTimeout(r, intervalMs * 3 + 5));
  clearInterval(id);
  assert.ok(count >= 2, `expected >=2 heartbeats, got ${count}`);
});

test('heartbeat interval stops after clearInterval', async () => {
  let count = 0;
  const intervalMs = 20;
  const id = setInterval(() => { count++; }, intervalMs);
  await new Promise<void>(r => setTimeout(r, intervalMs + 5));
  clearInterval(id);
  const before = count;
  await new Promise<void>(r => setTimeout(r, intervalMs * 2));
  assert.equal(count, before, 'interval should not fire after clearInterval');
});
