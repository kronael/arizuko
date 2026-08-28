// guards.ts — the two guard POLICIES that REFUSE. The table saying where they
// bind lives in policy.ts; the scanners are pure functions in skillguard.ts.
// Telemetry and UX policies (tool-log, todo-status, steering, pre-compact) only
// observe, so they stay out of this file.

import fs from 'fs';
import path from 'path';
import { ALLOW, ToolEvent, Verdict } from './policy.js';
import { blockingVerdict, isGuardedPath } from './skillguard.js';

// SECRET_ENV_VARS never reach a shell the agent drives. The model credential is
// spent by the SDK subprocess itself; a Bash call has no business reading it.
const SECRET_ENV_VARS = ['ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN'];

// bashScrub prefixes every Bash command with an unset of the model credentials.
// The one policy that returns 'rewrite', and rewrite is claude-only.
//
// INCOMPLETE, both ways (BUGS L131). This list is a second declaration of
// "model credential" and holds 2 of the 5 in store.EnvProfileKeys, so
// ANTHROPIC_OAUTH_TOKEN, OPENAI_API_KEY and CODEX_API_KEY survive the unset
// even on claude. And codex and pi honour no rewrite, so runnableBy drops the
// row there and all five stay readable — readSecrets puts every key set in the
// host .env into the container whatever ARIZUKO_BACKEND says.
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

// bashSkillRescan closes BUGS J8: the agent runs with bypassPermissions, so
// `printf '…' > ~/.claude/skills/x/SKILL.md` lands a persistent instruction with
// no scan at all.
//
// It judges the FILESYSTEM after the command rather than parsing the shell —
// redirection, tee, cp, mv, python and a heredoc all reach the same file. Any
// guarded file whose mtime moved this turn is rescanned, and a dangerous one is
// renamed aside: a skill only runs if it is discoverable at its own path.
// Quarantine rather than delete, so the operator can read what was refused.
export function bashSkillRescan(home: string): (ev: ToolEvent) => Verdict {
  // Starts at 0, not now: the first call of a session sweeps the whole tree. A
  // file written by a session that died before its post-hook ran is exactly the
  // one that gets loaded next session.
  let lastScan = 0;
  return (_ev: ToolEvent) => {
    const since = lastScan;
    lastScan = Date.now();
    try {
      for (const root of guardedRoots(home)) {
        for (const file of walkFiles(root)) {
          // A quarantined file is already out of discovery. Rescanning it renamed it
          // AGAIN on every sweep — and codex sweeps per tool call, one process each.
          if (file.endsWith('.quarantined')) continue;
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
          // The rescan holds the whole file by construction, so whole=true — the same
          // verdict the write gate and the load-time scan apply.
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
