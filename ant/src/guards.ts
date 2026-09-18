// guards.ts — the three guard POLICIES that REFUSE. The table saying where they
// bind lives in policy.ts; the scanners are pure functions in skillguard.ts.
// Telemetry and UX policies (tool-log, todo-status, steering, pre-compact) only
// observe, so they stay out of this file.

import fs from 'fs';
import path from 'path';
import { ALLOW, ToolEvent, Verdict } from './policy.js';
import { blockingVerdict, isGuardedPath } from './skillguard.js';

// SECRET_ENV_VARS never reach a shell the agent drives. The model credential is
// spent by the harness subprocess itself; a Bash call has no business reading it.
//
// This is the ANTHROPIC subset of store.EnvProfileKeys, and the split is
// deliberate: OPENAI_API_KEY and CODEX_API_KEY are in that set too, but the
// agent's own /oracle skill shells out to `codex` and probes exactly those two
// (ant/skills/oracle/SKILL.md), so scrubbing them would break a shipped skill.
// A model credential the agent has no tool for is scrubbed; one it drives a CLI
// with is not. guards.test.ts pins the partition so a key added to
// EnvProfileKeys cannot silently land on the wrong side (BUGS L131).
export const SECRET_ENV_VARS = [
  'ANTHROPIC_API_KEY',
  'CLAUDE_CODE_OAUTH_TOKEN',
  'ANTHROPIC_OAUTH_TOKEN',
];

// bashScrub prefixes every Bash command with an unset of the model credentials.
// The one policy that returns 'rewrite', and rewrite is claude-only — so this
// runs on claude and nowhere else.
//
// The other two harnesses do not need it for the same reason, and the reason
// matters (BUGS L131). codex filters its OWN shell tool's env: measured in
// codex-cli 0.147.0, shell_environment_policy carries default excludes `*KEY*`,
// `*SECRET*`, `*TOKEN*`, which cover all five EnvProfileKeys. pi does NOT —
// its getShellEnv() returns `{...process.env}` verbatim (utils/shell.js:105),
// so on pi every model credential is readable from the agent's bash tool and
// this gate is absent. That half of L131 is open and needs sign-off: pi's hook
// can only answer {block, reason}, and the credential must stay in pi's own env
// for pi to authenticate at all.
export function bashScrub(ev: ToolEvent): Verdict {
  const command = (ev.tool_input as { command?: string })?.command;
  if (!command) return ALLOW;

  const unsetPrefix = `unset ${SECRET_ENV_VARS.join(' ')} 2>/dev/null; `;
  return {
    kind: 'rewrite',
    input: { ...ev.tool_input, command: unsetPrefix + command },
  };
}

// GH_DENIED are the `gh` spellings the agent may not run once a folder holds a
// GitHub shell credential (spec 5/13 §Shell credentials). Each entry is the
// subcommand path plus, optionally, a flag that must also be present.
//
// Deny, not rewrite, so the row runs on all three harnesses — codex and pi can
// both refuse a tool call, and only `rewrite` is claude-only.
const GH_DENIED: Array<{ path: string[]; flag?: string; why: string }> = [
  { path: ['pr', 'review'], flag: '--approve', why: 'approving a pull request' },
  { path: ['pr', 'merge'], why: 'merging a pull request' },
  { path: ['pr', 'create'], why: 'opening a pull request' },
  { path: ['release', 'create'], why: 'cutting a release' },
  { path: ['repo', 'create'], why: 'creating a repository' },
  { path: ['repo', 'delete'], why: 'deleting a repository' },
];

// ghSegments splits a command into the pieces a separate program runs in. Crude
// on purpose — it does not honour quoting, so a `gh` inside a string literal is
// read as a call. Over-refusing a rare spelling costs a retry; the alternative
// is a shell parser in a guard.
function ghSegments(command: string): string[][] {
  return command
    .split(/[\n;|&]+/)
    .map((seg) => seg.trim().split(/\s+/).filter(Boolean))
    .filter((toks) => toks.length > 0);
}

// ghWriteGate refuses the GitHub writes a folder's agent must not make. It is
// DEFENCE IN DEPTH and explicitly not the lock: once a token is in the shell the
// agent spends it with `curl` as easily as with `gh`, and the approval this gate
// names is also reachable through `gh api …/reviews --input -`, where the event
// lives in stdin and no command string shows it. That spelling stays allowed
// because ant/skills/gh-comment posts review COMMENTS through it, and breaking a
// shipped skill to raise the cost of one bypass is the wrong trade.
//
// The lock is the token's own scope: a PAT minted without `pull_requests: write`
// cannot approve however it is called. The reason text says so, so an agent that
// hits this gate stops rather than hunting for the way around it.
//
// A FOLDER CAN HOLD THE CAPABILITY. `gh:write` on the folder resolves at
// dispatch and arrives as ARIZUKO_GH_WRITE, the same path every other 5/33
// capability takes. Without it there was nothing to grant: the gate refused
// every folder and the only way through was editing this list.
export function ghWriteGate(ev: ToolEvent): Verdict {
  if (process.env.ARIZUKO_GH_WRITE === '1') return ALLOW;
  const command = (ev.tool_input as { command?: string })?.command;
  if (!command) return ALLOW;

  for (const toks of ghSegments(command)) {
    // Leading `VAR=v`, `sudo`, `env` and `time` all precede the real command word.
    let i = 0;
    while (i < toks.length && (/^[A-Za-z_][A-Za-z0-9_]*=/.test(toks[i]) || ['sudo', 'env', 'time'].includes(toks[i]))) i++;
    const cmd = toks[i];
    if (cmd !== 'gh' && !cmd?.endsWith('/gh')) continue;

    const words = toks.slice(i + 1).filter((t) => !t.startsWith('-'));
    const flags = toks.slice(i + 1).filter((t) => t.startsWith('-'));
    for (const rule of GH_DENIED) {
      if (!rule.path.every((p, n) => words[n] === p)) continue;
      if (rule.flag && !flags.includes(rule.flag)) continue;
      return {
        kind: 'deny',
        reason:
          `Refused: ${rule.why} is not a capability this folder holds ` +
          `(ask an operator for \`gh:write\` on this folder) ` +
          `(gh ${rule.path.join(' ')}${rule.flag ? ' ' + rule.flag : ''}). ` +
          `Do not look for another route to the same effect — the GitHub token ` +
          `is scoped to refuse it too. Report what you wanted to do instead.`,
      };
    }
  }
  return ALLOW;
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
