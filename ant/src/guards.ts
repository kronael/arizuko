// guards.ts — the two guard POLICIES that live nowhere else. The table that
// says where they bind, and every other capability's row, is policy.ts.
//
// Before this file the guards were scattered: the bash secret-scrub lived
// inline in claude.ts, the skill scanner registered itself from skillguard.ts,
// and nothing listed what was actually guarded. You could not answer "what
// stops the agent doing X?" without reading the SDK call site. A guard nobody
// can find is a guard nobody maintains — and the two holes this file closes
// (BUGS J8, J9) both survived because the coverage was never written down in
// one place.
//
// The split: the SCANNERS are pure functions elsewhere (skillguard.ts owns the
// pattern table and the verdict), this file owns the two policies that REFUSE,
// and policy.ts owns the one table naming where each binds.
//
// Telemetry and UX policies (tool-log, todo-status, steering, pre-compact) are
// deliberately NOT here. They observe; they never refuse. Mixing them in would
// blur the question this file exists to answer.

import fs from 'fs';
import path from 'path';
import { ALLOW, ToolEvent, Verdict } from './policy.js';
import { blockingVerdict, isGuardedPath } from './skillguard.js';

// SECRET_ENV_VARS never reach a shell the agent drives. The model credential is
// spent by the SDK subprocess itself; a Bash call has no business reading it.
const SECRET_ENV_VARS = ['ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN'];

// bashScrub prefixes every Bash command with an unset of the model credentials.
// It is the one policy that returns 'rewrite', and rewrite is claude-only:
// codex's PreToolUse decision is approve|block|allow and its updatedInput lives
// on a different event. That costs nothing here — codex authenticates from
// $CODEX_HOME/auth.json, so there is no credential in its environment to strip.
export function bashScrub(ev: ToolEvent): Verdict {
  const command = (ev.tool_input as { command?: string })?.command;
  if (!command) return ALLOW;

  const unsetPrefix = `unset ${SECRET_ENV_VARS.join(' ')} 2>/dev/null; `;
  return {
    kind: 'rewrite',
    input: { ...ev.tool_input, command: unsetPrefix + command },
  };
}

// guardedRoots are the trees a skill can be loaded from. Kept narrow on
// purpose: everything else the agent writes is data.
function guardedRoots(home: string): string[] {
  return [path.join(home, '.claude', 'skills')];
}

// walkFiles lists every regular file under root. Missing root is not an error —
// a group with no skills directory is the normal case.
function walkFiles(root: string, out: string[] = []): string[] {
  let entries: fs.Dirent[];
  try {
    entries = fs.readdirSync(root, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const e of entries) {
    const p = path.join(root, e.name);
    if (e.isDirectory()) walkFiles(p, out);
    else if (e.isFile()) out.push(p);
  }
  return out;
}

// createBashSkillRescanHook closes BUGS J8: skill-guard hooks Write/Edit/
// MultiEdit, but the agent runs with sandbox disabled and bypassPermissions, so
// `printf '…' > ~/.claude/skills/x/SKILL.md` lands a persistent, executable
// instruction with no scan at all.
//
// It runs AFTER the command rather than trying to parse the shell. Reading a
// command and deciding whether it writes a path is a losing game — redirection,
// tee, cp, mv, python, a heredoc inside a subshell all reach the same file. So
// the guard judges the FILESYSTEM, which has no such ambiguity: any guarded
// file whose mtime moved during this turn is rescanned, and a dangerous one is
// renamed aside before the next session can load it.
//
// Quarantine rather than delete: the agent's work is never destroyed, and the
// operator can read what was refused. The rename is what matters — a skill only
// runs if it is discoverable at its own path.
export function bashSkillRescan(home: string): (ev: ToolEvent) => Verdict {
  // Starts at 0, not now: the first Bash call of a session sweeps the whole
  // tree. A file written by a session that died before its post-hook ran would
  // otherwise never be looked at again, and that file is exactly the one that
  // gets loaded next session. After the first sweep the mtime gate keeps the
  // cost to files that actually moved.
  let lastScan = 0;
  return (_ev: ToolEvent) => {
    const since = lastScan;
    lastScan = Date.now();
    try {
      for (const root of guardedRoots(home)) {
        for (const file of walkFiles(root)) {
          let st: fs.Stats;
          try {
            st = fs.statSync(file);
          } catch {
            continue;
          }
          if (st.mtimeMs < since) continue;

          let text: string;
          try {
            text = fs.readFileSync(file, 'utf-8');
          } catch {
            continue;
          }
          // The rescan holds the whole file by construction (it read it off
          // disk), so whole=true and blockingVerdict's SKILL.md condition
          // decides the frontmatter half — the same verdict the write gate
          // and the load-time scan apply.
          const blocking = blockingVerdict(file, text, true);
          if (blocking.length === 0) continue;

          const aside = `${file}.quarantined`;
          fs.renameSync(file, aside);
          console.error(
            `skillguard: quarantined ${file} -> ${aside} — written outside the ` +
              `write gate and it scans dangerous: ` +
              blocking.map((f) => `${f.severity} ${f.patternId}`).join(', '),
          );
        }
      }
    } catch (err) {
      // Fails OPEN, like the write gate: a guard that bricks the turn when it
      // throws is worse than the threat it was added for.
      console.error(`skillguard: rescan failed: ${err}`);
    }
    return ALLOW;
  };
}

export { isGuardedPath };
