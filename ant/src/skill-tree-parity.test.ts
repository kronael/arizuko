// Three skill trees exist twice: once at the top level as a dispatchable
// SKILL.md, once inside the `create` router's subtree as a reference the
// router reads (`create/CLAUDE.md` lists them). Both copies are live, so a
// change applied to one and not the other is a silent behaviour split.
//
// The p5js pair already split that way: the top-level export-frames.js
// pinned puppeteer to the image's apt-installed chromium, the create copy
// did not, so a sketch exported through /create tried to download
// puppeteer's bundled build — which the egress allowlist (anthropic.com
// only, default deny) refuses. These tests hold the copies byte-equal.

import { test, expect } from 'bun:test';
import fs from 'fs';
import path from 'path';

const SKILLS = path.join(import.meta.dir, '..', 'skills');

// The top-level copy is the dispatchable skill, the nested one is the
// create router's reference; that is the ONLY difference either may carry.
const MARKERS = new Set(['SKILL.md', 'README.md']);

const PAIRS: Array<{ name: string; top: string; nested: string }> = [
  { name: 'p5js', top: 'p5js', nested: 'create/art/p5js' },
  { name: 'excalidraw', top: 'excalidraw', nested: 'create/diagram/excalidraw' },
  { name: 'manim', top: 'manim-video', nested: 'create/video/manim' },
];

function walk(root: string): string[] {
  const out: string[] = [];
  const recurse = (dir: string) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) recurse(full);
      else out.push(path.relative(root, full));
    }
  };
  recurse(root);
  return out.sort();
}

for (const pair of PAIRS) {
  const top = path.join(SKILLS, pair.top);
  const nested = path.join(SKILLS, pair.nested);

  test(`${pair.name}: both trees exist, so neither copy is dead`, () => {
    expect(fs.existsSync(top)).toBe(true);
    expect(fs.existsSync(nested)).toBe(true);
  });

  test(`${pair.name}: the two trees hold the same files, bar the dispatch marker`, () => {
    const only = (a: string[], b: string[]) =>
      a.filter((f) => !b.includes(f)).filter((f) => !MARKERS.has(path.basename(f)));
    const topFiles = walk(top);
    const nestedFiles = walk(nested);
    expect({ onlyTop: only(topFiles, nestedFiles), onlyNested: only(nestedFiles, topFiles) })
      .toEqual({ onlyTop: [], onlyNested: [] });
  });

  test(`${pair.name}: every shared file is byte-identical in both trees`, () => {
    const nestedFiles = new Set(walk(nested));
    const drifted = walk(top)
      .filter((f) => nestedFiles.has(f) && !MARKERS.has(path.basename(f)))
      .filter((f) => !fs.readFileSync(path.join(top, f)).equals(fs.readFileSync(path.join(nested, f))));
    expect(drifted).toEqual([]);
  });
}

// The specific split that cost a live export, named so a revert fails loudly
// rather than only widening the generic parity diff above.
test('p5js: both export-frames.js copies pin chromium instead of downloading it', () => {
  for (const dir of ['p5js', 'create/art/p5js']) {
    const script = fs.readFileSync(path.join(SKILLS, dir, 'scripts', 'export-frames.js'), 'utf8');
    expect(script).toContain("process.env.PUPPETEER_EXECUTABLE_PATH || '/usr/bin/chromium'");
  }
});
