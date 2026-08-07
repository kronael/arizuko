// skillguard.ts — spec 5/23. An agent can write a skill that runs in its NEXT
// session, so a skill file is code the agent grants itself. This is the only
// gate between "the agent wrote it" and "the agent runs it": a PreToolUse hook
// on Write/Edit/MultiEdit that scans content headed for ~/.claude/**.
//
// The regex table is ported verbatim from hermes-agent (see
// skillguard-patterns.ts). It is not rewritten — a retyped table is a new table
// with new gaps.

import { HookCallback, PreToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';
import { THREAT_PATTERNS, ThreatPattern, Severity } from './skillguard-patterns.js';

export interface Finding {
  patternId: string;
  severity: Severity;
  category: string;
  line: number;
  match: string;
  description: string;
}

export type Verdict = 'safe' | 'caution' | 'dangerous';

// Compiled once. An entry whose regex the JS engine rejects is dropped with a
// warning rather than throwing at import: one bad pattern must not disarm the
// other 119.
const COMPILED = THREAT_PATTERNS.flatMap((p: ThreatPattern) => {
  try {
    return [{ ...p, rx: new RegExp(p.re, 'i') }];
  } catch (err) {
    console.error(`skillguard: pattern ${p.id} failed to compile: ${err}`);
    return [];
  }
});

// U+200B..U+FEFF zero-width, bidi-override and invisible-math characters. Text
// that renders as one thing and means another is how a skill hides an
// instruction from the human reviewing it.
const INVISIBLE_CHARS = new Set([
  '​', '‌', '‍', '⁠', '⁢', '⁣', '⁤',
  '‪', '‫', '‬', '‭', '‮',
  '⁦', '⁧', '⁨', '⁩', '﻿',
]);

// scanContent runs the table over one file's text. Line numbers are 1-based so
// they match what an editor shows.
export function scanContent(text: string): Finding[] {
  const findings: Finding[] = [];
  const lines = text.split('\n');
  lines.forEach((line, i) => {
    for (const p of COMPILED) {
      const m = p.rx.exec(line);
      if (!m) continue;
      findings.push({
        patternId: p.id,
        severity: p.severity,
        category: p.category,
        line: i + 1,
        match: m[0].slice(0, 120),
        description: p.description,
      });
    }
    for (const ch of line) {
      if (!INVISIBLE_CHARS.has(ch)) continue;
      findings.push({
        patternId: 'invisible_unicode',
        severity: 'high',
        category: 'obfuscation',
        line: i + 1,
        match: `U+${ch.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}`,
        description: 'invisible or bidi-override character in skill text',
      });
      break;
    }
  });
  return findings;
}

const MAX_DESCRIPTION = 1024;

// validateFrontmatter enforces SKILL.md's contract: `name` and `description`
// are what skill dispatch matches on, so a skill missing either is invisible to
// the agent that wrote it — a silent no-op, not an error it would ever see.
export function validateFrontmatter(text: string): Finding[] {
  const findings: Finding[] = [];
  const fm = /^---\n([\s\S]*?)\n---/.exec(text);
  if (!fm) {
    return [{
      patternId: 'frontmatter_missing',
      severity: 'high',
      category: 'structural',
      line: 1,
      match: '',
      description: 'SKILL.md has no --- frontmatter block',
    }];
  }
  const body = fm[1];
  const name = /^name:\s*(.+)$/m.exec(body);
  const description = /^description:\s*(.+)$/m.exec(body);
  if (!name || !name[1].trim()) {
    findings.push({
      patternId: 'frontmatter_no_name',
      severity: 'high',
      category: 'structural',
      line: 1,
      match: '',
      description: 'SKILL.md frontmatter has no `name:`',
    });
  }
  if (!description || !description[1].trim()) {
    findings.push({
      patternId: 'frontmatter_no_description',
      severity: 'high',
      category: 'structural',
      line: 1,
      match: '',
      description: 'SKILL.md frontmatter has no `description:` — dispatch matches on it',
    });
  } else if (description[1].trim().length > MAX_DESCRIPTION) {
    findings.push({
      patternId: 'frontmatter_description_long',
      severity: 'medium',
      category: 'structural',
      line: 1,
      match: `${description[1].trim().length} chars`,
      description: `description exceeds ${MAX_DESCRIPTION} characters`,
    });
  }
  return findings;
}

export function verdictOf(findings: Finding[]): Verdict {
  if (findings.length === 0) return 'safe';
  return findings.some((f) => f.severity === 'critical') ? 'dangerous' : 'caution';
}

// isGuardedPath — the hook only governs the agent's own config tree. Everything
// else it writes (its group home, public_html) is data, gated elsewhere.
export function isGuardedPath(p: string): boolean {
  return p.includes('/.claude/');
}

// writtenText pulls the text a Write/Edit/MultiEdit would land. An unknown
// tool shape yields nothing, which fails open by construction.
export function writtenText(toolName: string, input: Record<string, unknown>): string {
  if (toolName === 'Write') return String(input.content ?? '');
  if (toolName === 'Edit') return String(input.new_string ?? '');
  if (toolName === 'MultiEdit') {
    const edits = input.edits;
    if (!Array.isArray(edits)) return '';
    return edits.map((e) => String((e as Record<string, unknown>)?.new_string ?? '')).join('\n');
  }
  return '';
}

function report(path: string, findings: Finding[]): string {
  const lines = findings
    .slice(0, 10)
    .map((f) => `  ${f.severity} ${f.patternId} (line ${f.line}): ${f.description} — ${f.match}`);
  const more = findings.length > 10 ? `\n  …and ${findings.length - 10} more` : '';
  return `skill-guard refused this write to ${path}:\n${lines.join('\n')}${more}\n` +
    'Rewrite without the flagged content, or ask the operator to make the change.';
}

// createSkillGuardHook denies a write whose content scans `dangerous` (any
// critical finding). `caution` passes: for a write gate on the agent's own
// skills, blocking legitimate work is the more expensive failure, so the
// false-positive budget is spent only on critical patterns.
//
// A crash in the scanner fails OPEN for the same reason — a guard that bricks
// the agent when it throws is worse than the threat it was added for.
export function createSkillGuardHook(): HookCallback {
  return async (input, _toolUseId, _context) => {
    try {
      const pre = input as PreToolUseHookInput;
      const toolInput = (pre.tool_input ?? {}) as Record<string, unknown>;
      const path = String(toolInput.file_path ?? '');
      if (!path || !isGuardedPath(path)) return {};

      const text = writtenText(pre.tool_name, toolInput);
      if (!text) return {};

      const findings = scanContent(text);
      if (path.endsWith('SKILL.md')) findings.push(...validateFrontmatter(text));
      if (verdictOf(findings) !== 'dangerous') return {};

      const critical = findings.filter((f) => f.severity === 'critical');
      return {
        hookSpecificOutput: {
          hookEventName: 'PreToolUse',
          permissionDecision: 'deny',
          permissionDecisionReason: report(path, critical),
        },
      };
    } catch (err) {
      console.error(`skillguard: scan failed, allowing write: ${err}`);
      return {};
    }
  };
}
