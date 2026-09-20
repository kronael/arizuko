// A skill that dispatches to a subagent type the container does not declare
// gets "Agent type 'sonnet' not found" from the SDK, and the work then runs at
// the parent's model and effort instead of the cheaper pinned one. That went
// unnoticed for weeks because nothing failed loudly (BUGS L77). This test reads
// the shipped corpus and fails when the two drift again.

import { test, expect } from 'bun:test';
import fs from 'fs';
import path from 'path';
import { AGENTS } from './agents.js';

const SKILLS_DIR = path.join(import.meta.dir, '..', 'skills');

// dispatchedTypes collects every subagent_type the shipped skills name.
function dispatchedTypes(): Map<string, string[]> {
  const found = new Map<string, string[]>();
  for (const entry of fs.readdirSync(SKILLS_DIR, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const file = path.join(SKILLS_DIR, entry.name, 'SKILL.md');
    if (!fs.existsSync(file)) continue;
    const body = fs.readFileSync(file, 'utf8');
    for (const match of body.matchAll(/subagent_type:\s*"?([a-zA-Z][\w-]*)"?/g)) {
      const type = match[1];
      // The negative form: browse/SKILL.md says it is NOT a subagent type.
      if (/never pass it to Agent/.test(body) && type === 'browse') continue;
      found.set(type, [...(found.get(type) ?? []), entry.name]);
    }
  }
  return found;
}

test('every subagent type a shipped skill dispatches to is declared', () => {
  const missing: string[] = [];
  for (const [type, skills] of dispatchedTypes()) {
    if (!(type in AGENTS)) missing.push(`${type} (named by ${skills.join(', ')})`);
  }
  expect(missing).toEqual([]);
});

test('the corpus really does dispatch, so the check above cannot pass vacuously', () => {
  expect(dispatchedTypes().size).toBeGreaterThan(3);
});

test('a tier agent pins effort, because effort is inherited when it is absent', () => {
  for (const name of ['fable', 'opus', 'sonnet', 'haiku']) {
    expect(AGENTS[name]?.model).toBeTruthy();
    expect(AGENTS[name]?.effort).toBeTruthy();
  }
});

test('a role agent names its skill instead of restating it', () => {
  for (const name of ['distill', 'improve', 'learn', 'readme', 'refine', 'visual']) {
    expect(AGENTS[name]?.skills).toEqual([name]);
    expect(AGENTS[name]?.prompt.length).toBeLessThan(200);
  }
});
