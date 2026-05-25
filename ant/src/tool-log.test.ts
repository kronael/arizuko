// Unit tests for the tool-log hook helpers. Pure functions only;
// no SDK runtime, no socket. Asserts the [ant] [tool] {json} line shape
// and secret-redaction boundaries.

import { test, expect, beforeEach } from 'bun:test';
import {
  buildToolLogLine,
  summarizeArgs,
  recordStart,
  _resetStartTimes,
} from './tool-log.js';

beforeEach(() => { _resetStartTimes(); });

test('summarizeArgs: Bash truncates command to 200 chars', () => {
  const cmd = 'echo ' + 'A'.repeat(500);
  const out = summarizeArgs('Bash', { command: cmd });
  expect(out.length).toBe(200);
  expect(out.startsWith('echo AAAA')).toBe(true);
});

test('summarizeArgs: Edit emits file_path, NEVER content', () => {
  const out = summarizeArgs('Edit', {
    file_path: '/home/node/secret.txt',
    old_string: 'API_KEY=sk-real-secret',
    new_string: 'API_KEY=sk-new-secret',
  });
  expect(out).toBe('/home/node/secret.txt');
  expect(out.includes('sk-')).toBe(false);
});

test('summarizeArgs: Write emits file_path only', () => {
  const out = summarizeArgs('Write', {
    file_path: '/tmp/x.json',
    content: 'PRIVATE_KEY=...',
  });
  expect(out).toBe('/tmp/x.json');
});

test('summarizeArgs: Read emits file_path', () => {
  expect(summarizeArgs('Read', { file_path: '/etc/passwd' })).toBe('/etc/passwd');
});

test('summarizeArgs: Grep emits pattern', () => {
  expect(summarizeArgs('Grep', { pattern: 'TODO' })).toBe('TODO');
});

test('summarizeArgs: unknown tool returns empty string', () => {
  expect(summarizeArgs('Mystery', { foo: 'bar' })).toBe('');
});

test('summarizeArgs: null/undefined input returns empty', () => {
  expect(summarizeArgs('Bash', null)).toBe('');
  expect(summarizeArgs('Bash', undefined)).toBe('');
});

test('buildToolLogLine: pre phase has the expected shape', () => {
  const now = Date.parse('2026-05-25T12:00:00.000Z');
  const line = buildToolLogLine('pre', 'Bash', { command: 'ls' }, 'sess-abc', 'tu-1', now);
  expect(line.ant).toBe('tool');
  expect(line.phase).toBe('pre');
  expect(line.turn_id).toBe('sess-abc');
  expect(line.tool).toBe('Bash');
  expect(line.args_summary).toBe('ls');
  expect(line.ts).toBe('2026-05-25T12:00:00.000Z');
  expect(line.duration_ms).toBeUndefined();
  expect(line.outcome).toBeUndefined();
});

test('buildToolLogLine: post computes duration_ms from recordStart', () => {
  const t0 = 1_000_000;
  recordStart('tu-2', t0);
  const line = buildToolLogLine('post', 'Bash', { command: 'sleep 1' }, 'sess', 'tu-2', t0 + 1234);
  expect(line.phase).toBe('post');
  expect(line.duration_ms).toBe(1234);
  expect(line.outcome).toBe('ok');
});

test('buildToolLogLine: post without recorded start has no duration_ms', () => {
  const line = buildToolLogLine('post', 'Read', { file_path: '/x' }, 'sess', 'tu-orphan', Date.now());
  expect(line.duration_ms).toBeUndefined();
  expect(line.outcome).toBe('ok');
});

test('buildToolLogLine: post detects is_error response', () => {
  const line = buildToolLogLine(
    'post', 'Bash', { command: 'false' }, 'sess', 'tu-3', Date.now(),
    { is_error: true, error: 'exit status 1' },
  );
  expect(line.outcome).toBe('error');
  expect(line.error).toBe('exit status 1');
});

test('buildToolLogLine: JSON.stringify emits a stable single-line shape', () => {
  const now = Date.parse('2026-05-25T12:00:00.000Z');
  const line = buildToolLogLine('pre', 'Bash', { command: 'ls' }, 'sess-1', 'tu-x', now);
  const s = JSON.stringify(line);
  expect(s.includes('\n')).toBe(false);
  expect(s).toContain('"ant":"tool"');
  expect(s).toContain('"phase":"pre"');
  expect(s).toContain('"turn_id":"sess-1"');
  expect(s).toContain('"tool":"Bash"');
});
