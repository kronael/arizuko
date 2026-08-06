// Resume-guard + delivery-gate contracts for the stale chat-bound session bug
// (BUGS: "Stale chat-bound session → error_during_execution + silent context
// loss"). resolveResumeSession must start fresh — never attempt --resume — when
// a stored UUID has no transcript on disk; isSessionError must classify a resume
// miss as a retry signal, so runQuery skips deliverTurn for it.

import { test, expect } from 'bun:test';
import path from 'path';
import { resolveResumeSession, isSessionError, transcriptPath } from './index.js';
import { claudeResultStatus } from './claude.js';

const UUID = 'b6c2b287-0000-4000-8000-000000000000';
const never = () => false;
const always = () => true;

test('resolveResumeSession: UUID with a transcript on disk resumes it', () => {
  expect(resolveResumeSession(UUID, always)).toBe(UUID);
});

test('resolveResumeSession: UUID with NO transcript starts fresh (no --resume)', () => {
  // The pruned/never-persisted case — must not be passed to --resume, which
  // would return "No conversation found" → error_during_execution.
  expect(resolveResumeSession(UUID, never)).toBeUndefined();
});

test('resolveResumeSession: non-UUID placeholder starts fresh even if a file exists', () => {
  expect(resolveResumeSession('sess-123abc', always)).toBeUndefined();
});

test('resolveResumeSession: undefined session id stays undefined', () => {
  expect(resolveResumeSession(undefined, always)).toBeUndefined();
});

test('resolveResumeSession: a non-UUID short-circuits before the disk check', () => {
  // The UUID shape is the cheap guard; a lineage placeholder must never cost a
  // stat call, and must never be resumed regardless of what is on disk.
  let calls = 0;
  const counting = () => { calls++; return true; };
  expect(resolveResumeSession('sess-placeholder', counting)).toBeUndefined();
  expect(calls).toBe(0);
});

test('transcriptPath uses the fixed -home-node slug', () => {
  expect(transcriptPath(UUID)).toBe(
    path.join('/home/node', '.claude', 'projects', '-home-node', `${UUID}.jsonl`),
  );
});

test('isSessionError: error_during_execution is a retry signal (never delivered as a reply)', () => {
  // runQuery delivers a turn only when !isSessionError(subtype); a resume miss
  // is not a user-facing error.
  expect(isSessionError('error_during_execution')).toBe(true);
});

test('isSessionError: a real result subtype is delivered', () => {
  expect(isSessionError('success')).toBe(false);
  expect(isSessionError(undefined)).toBe(false);
});

test('claudeResultStatus rejects the SDK login sentinel', () => {
  expect(claudeResultStatus('success', 'Not logged in · Please run /login')).toBe('error');
});

test('claudeResultStatus preserves normal result status', () => {
  expect(claudeResultStatus('success', 'completed')).toBe('success');
  expect(claudeResultStatus('error_max_turns', 'stopped')).toBe('error');
});
