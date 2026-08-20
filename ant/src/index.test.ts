// The chassis <status> pipeline: statusBlocks is THE one extraction site —
// every harness's status frames flow through it, so a harness only has to put
// its assistant text on the frame (the pre-seam runtime read claude's raw
// message shape instead, and codex streamed no interim status at all).

import { test, expect } from 'bun:test';
import { statusBlocks } from './index.js';

test('statusBlocks: extracts each trimmed block in order', () => {
  expect(statusBlocks('a <status>reading files…</status> b <status> two </status>'))
    .toEqual(['reading files…', 'two']);
});

test('statusBlocks: empty blocks and tagless text yield nothing', () => {
  expect(statusBlocks('<status>  </status>')).toEqual([]);
  expect(statusBlocks('no tags here')).toEqual([]);
});

test('statusBlocks: multiline block content survives', () => {
  expect(statusBlocks('<status>line one\nline two</status>')).toEqual(['line one\nline two']);
});
