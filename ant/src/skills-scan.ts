// skills-scan.ts — load-time skill gate for backends without a write-time
// PreToolUse hook (spec 6/7 § "Skills across backends"). codex and pi cannot
// run the 5/23 skill-guard hook, so the SAME scanner (skillguard.ts — one
// table, never a second copy) runs here at session start instead: a skill
// with a blocking finding is excluded from what the connector loads. This
// gates what is LOADED, not what the model later reads by path — narrower
// than the write-time gate, and the spec says so.

import fs from 'fs';
import path from 'path';
import { blockingFindings, scanContent, scalarOf, validateFrontmatter } from './skillguard.js';

export interface SkillRef {
  name: string;
  description: string;
  dir: string; // the skill's directory, containing SKILL.md
}

export interface SkillScan {
  clean: SkillRef[];
  // excluded lists blocked skills as "name: reason" for the log — the
  // load-time analogue of the write-time refusal message.
  excluded: string[];
}

// scanSkills discovers <dir>/<name>/SKILL.md one level deep — Claude Code's
// own discovery shape for ~/.claude/skills — and gates each file through the
// skillguard table plus the frontmatter contract. A missing SKILL.md is not a
// skill (seedSkills removes it for .disabled stock skills).
export function scanSkills(dir: string): SkillScan {
  const out: SkillScan = { clean: [], excluded: [] };
  let entries: fs.Dirent[];
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return out; // no skills dir → no skills
  }
  for (const e of entries) {
    if (!e.isDirectory()) continue;
    const skillDir = path.join(dir, e.name);
    let text: string;
    try {
      text = fs.readFileSync(path.join(skillDir, 'SKILL.md'), 'utf-8');
    } catch {
      continue;
    }
    const blocking = blockingFindings([...scanContent(text), ...validateFrontmatter(text)]);
    if (blocking.length > 0) {
      const f = blocking[0];
      out.excluded.push(`${e.name}: ${f.patternId} (${f.description})`);
      continue;
    }
    const fm = /^---\n([\s\S]*?)\n---/.exec(text);
    const body = fm ? fm[1] : '';
    out.clean.push({
      // validateFrontmatter passed, so name/description exist; the fallbacks
      // are for the type system, not a data path.
      name: scalarOf(body, 'name') ?? e.name,
      description: scalarOf(body, 'description') ?? '',
      dir: skillDir,
    });
  }
  return out;
}

// renderSkillsIndex is the prompt-side skill surface for a backend with no
// native skill mechanism (codex): a skill reduced to its essence — a named
// instruction file plus a trigger. The model reads the file on match.
export function renderSkillsIndex(skills: SkillRef[]): string {
  if (skills.length === 0) return '';
  const rows = skills.map((s) => `- ${s.name} — ${s.description} → ${path.join(s.dir, 'SKILL.md')}`);
  return [
    '<skills>',
    'Named instruction files. When the task matches a description, read that SKILL.md and follow it.',
    ...rows,
    '</skills>',
  ].join('\n');
}
