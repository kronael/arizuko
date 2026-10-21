import { expect, test } from 'bun:test';
import fs from 'fs';

/** The old manual told agents final prose was delivered; no resident copy may retain that contract. */
test('resident manual describes explicit delivery without think-tag protocol', () => {
  const manual = fs.readFileSync(new URL('../CLAUDE.md', import.meta.url), 'utf8');
  expect(manual).toContain('# Unattended');
  expect(manual).toContain('No one reads this terminal');
  expect(manual).toContain('`reply` (a `like` counts)');
  expect(manual).not.toContain('<think>');
  expect(manual).not.toContain('Only your FINAL text block is delivered');
  expect(manual).not.toContain('REPLACES the answer');
});
