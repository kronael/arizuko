// The spool is the queue that crosses codex's hook process boundary, so its
// contract is delivery discipline: a published batch is read whole, exactly
// once, in enqueue order — even with a writer appending mid-drain, which is
// why a queue is a directory of batch files, not one truncated file.

import { expect, test } from 'bun:test';
import fs from 'fs';
import os from 'os';
import path from 'path';
import { appendLines, drainLines } from './spool.js';

function tmpSpool(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'spool-'));
}

test('batches drain whole and in enqueue order', () => {
  const dir = tmpSpool();
  appendLines(dir, ['one', 'two']);
  appendLines(dir, ['three']);
  expect(drainLines(dir)).toEqual(['one', 'two', 'three']);
});

test('a drained queue is empty — a message is delivered once, never replayed', () => {
  const dir = tmpSpool();
  appendLines(dir, ['only once']);
  expect(drainLines(dir)).toEqual(['only once']);
  expect(drainLines(dir)).toEqual([]);
});

test('a message containing a newline survives the round trip', () => {
  const dir = tmpSpool();
  appendLines(dir, ['line one\nline two']);
  expect(drainLines(dir)).toEqual(['line one\nline two']);
});

test('an absent queue directory is nothing queued, not an error', () => {
  expect(drainLines(path.join(os.tmpdir(), 'spool-never-created'))).toEqual([]);
});

test('an empty append publishes nothing', () => {
  const dir = tmpSpool();
  appendLines(dir, []);
  expect(fs.readdirSync(dir)).toEqual([]);
});

// The single-file spool lost a batch appended between the reader's read and
// truncate. With per-batch files a batch published mid-drain is simply a batch
// the NEXT drain returns — never silently gone.
test('a batch published after a drain is returned by the next drain', () => {
  const dir = tmpSpool();
  appendLines(dir, ['early']);
  expect(drainLines(dir)).toEqual(['early']);
  appendLines(dir, ['late']);
  expect(drainLines(dir)).toEqual(['late']);
});

// A half-written batch must never be read: writes publish by rename, so a
// dot-prefixed temp file is invisible to the drain.
test('an unpublished temp file is not drained', () => {
  const dir = tmpSpool();
  fs.writeFileSync(path.join(dir, '.123-1-000000.tmp'), '"half"');
  expect(drainLines(dir)).toEqual([]);
});
