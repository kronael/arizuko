// Backend selection: default claude, codex by name, unknown = fatal. Spec 5/K.

import { test, expect } from 'bun:test';
import { selectBackend } from './index.js';

const noDrain = () => [];

test('selectBackend: default (undefined) is claude', () => {
  const b = selectBackend(undefined, noDrain);
  expect(b.name()).toBe('claude');
});

test('selectBackend: empty string is claude', () => {
  expect(selectBackend('', noDrain).name()).toBe('claude');
});

test('selectBackend: "codex" selects codex', () => {
  const b = selectBackend('codex', noDrain);
  expect(b.name()).toBe('codex');
});

test('selectBackend: whitespace is trimmed', () => {
  expect(selectBackend('  codex  ', noDrain).name()).toBe('codex');
});

test('selectBackend: unknown value is fatal (throws, no silent fallback)', () => {
  expect(() => selectBackend('gpt4all', noDrain)).toThrow(/unknown ARIZUKO_BACKEND/);
});

test('both backends advertise the full capability surface', () => {
  for (const name of ['claude', 'codex']) {
    const caps = selectBackend(name, noDrain).capabilities();
    expect(caps.streaming).toBe(true);
    expect(caps.interrupt).toBe(true);
    expect(caps.multiTurn).toBe(true);
    expect(caps.toolUse).toBe(true);
    expect(caps.sessionResume).toBe(true);
    expect(caps.mcpClient).toBe(true);
    expect(caps.permissionPrompt).toBe(true);
  }
});
