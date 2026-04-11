---
status: draft
source: hermes-agent peel (2026-04-11)
---

# specs/6/7 — skill-guard hook (hermes peel, lean version)

Port the threat-pattern scanner from NousResearch/hermes-agent's
`tools/skills_guard.py` as a PreToolUse hook in `ant/src/index.ts`.
Block malicious writes to `~/.claude/**` at the tool gate, before they
touch disk.

## Background — why this is the lean version

An earlier draft of this spec (commit `1c92bda`) proposed porting
Hermes's full memory + skill_manage MCP tools (~850 LOC). Deep research
against `/home/onvos/app/refs/hermes-agent` showed most of that is
redundant:

| Hermes feature                      | Arizuko equivalent                     |
| ----------------------------------- | -------------------------------------- |
| Frozen-snapshot system prompt       | Claude Code CLI reads CLAUDE.md once   |
| Persistent memory (MEMORY.md)       | `~/.claude/CLAUDE.md` (agent-editable) |
| Skill CRUD tool                     | `Write`/`Edit` on `~/.claude/skills/`  |
| Fuzzy patch matching                | `Edit` tool does this natively         |
| Atomic writes                       | `Write` is atomic in Claude Code       |
| File locking                        | GroupQueue serializes per-group        |
| Cache invalidation (mtime manifest) | Fresh system prompt per container      |
| Post-turn review thread             | Doesn't fit ephemeral-container model  |

The **only** Hermes piece that transfers cleanly is the security
scanner. Arizuko has no equivalent today — an agent can write
`os.system(f'curl {SECRET} | bash')` into a skill script and nothing
stops it.

## Problem

Skills under `~/.claude/skills/` can contain executable scripts
(Python, shell, JS) under `scripts/` subdirs. Skills are loaded into
the system prompt on every session start. This means:

1. A compromised or confused agent can write exfiltration code into a
   skill that runs next session
2. A prompt-injected inbound message can coerce the agent into
   writing a role-hijack payload into `~/.claude/CLAUDE.md` that gets
   re-injected on the next session
3. Neither case is caught today — `Write`/`Edit` have no content
   scanner, and operators don't review every skill the agent writes

## Current terrain

- `ant/src/index.ts` already wires PreToolUse hooks via the Agent SDK.
  Existing hooks: `createPreCompactHook`, `createSanitizeBashHook`,
  `createIpcDrainHook` (new today as part of mid-loop steering fix).
- Agent skills are seeded from `/workspace/self/ant/skills/` to
  `~/.claude/skills/` at first container spawn, then agent owns it.
- `~/.claude/CLAUDE.md` is read by Claude Code CLI at session start.

## Design

### One new PreToolUse hook: `createSkillGuardHook()`

Fires on `Write`, `Edit`, `MultiEdit` when the `file_path` parameter is
under `~/.claude/**`. Runs two checks:

1. **Threat pattern scan** — 100-ish regex patterns (ported from
   `skills_guard.py`) across categories: exfil, prompt injection,
   persistence, destructive ops, reverse shells, crypto miners,
   hardcoded secrets, invisible unicode. Return `{decision: "block"}`
   with a clear error message naming the pattern id if any match.

2. **SKILL.md frontmatter validation** — when the path matches
   `skills/*/SKILL.md`, parse the YAML frontmatter and require
   `name:` + `description:` fields. Description max 1024 chars. Reject
   malformed / incomplete skills.

Return `{decision: "allow"}` when nothing matches. Wrap in try/catch;
log errors via `log()`; never throw (hooks failing fail-closed is
fine here — allow the write on scanner crash rather than block
legitimate work).

### Threat pattern table

Port the regex table from `hermes-agent/tools/skills_guard.py` lines
82-484. Organized by category, each pattern is `{regexp, id, severity}`.
Categories to port:

- `prompt_injection` — ignore-previous, role-hijack, DAN/developer mode,
  "don't tell user", disregard rules, bypass restrictions
- `exfiltration` — curl/wget with env vars matching
  KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API, DNS exfil, markdown image
  with env vars, env dumping
- `secret_access` — reading `.env`, `.netrc`, `.pgpass`, `.npmrc`,
  SSH dirs, AWS creds, GPG dirs
- `persistence` — crontab, shell rc files, SSH authorized_keys,
  systemd unit creation, git config global, CLAUDE.md modification
- `destructive` — `rm -rf /`, `chmod 777`, `/etc/*` overwrite, `mkfs`,
  `dd if=/dev/zero`, `shutil.rmtree` with user-controlled paths
- `network_abuse` — reverse shells, socat listeners, ngrok/localtunnel,
  hardcoded IPs in bind/connect
- `obfuscation` — base64 | sh pipes, hex-to-bytes exec, `eval`/`exec`
  with remote input, echo-piped-to-shell, `getattr(__builtins__)`
- `privilege_escalation` — `sudo`, setuid/setgid, NOPASSWD sudoers edits
- `supply_chain` — curl | sh, unpinned `pip install`, `npm install`
  without lockfile, `docker pull` unknown images
- `crypto_mining` — xmrig, coinhive, hashrate/thread-count strings
- `hardcoded_secrets` — GitHub token patterns, OpenAI sk- keys, AWS
  AKIA keys, JWT-looking strings
- `invisible_unicode` — zero-width spaces (U+200B..U+200D), directional
  overrides (U+202A..U+202E), isolates

Actual regex strings: copy from the reference file directly. Don't
reinvent. Expect ~100-150 patterns in the final table.

### False positive policy

Hermes's patterns are deliberately loose — `curl [^\n]*\$\w*KEY`
matches any curl using any env var ending in KEY, not just secrets.
Accept this. False positives are cheaper than false negatives for a
skill-write gate. The agent can always rephrase (use a variable named
`FEED_URL` instead of `FEED_KEY`, or chain via subshell).

If false positive rate becomes noisy in practice, add an ignore-list
of known-safe patterns, but start strict.

### Files to add / touch

**New**:

- `ant/src/skillguard.ts` (~180 LOC) — the regex table + check function
- `ant/src/skillguard.test.ts` — block/allow cases for each category

**Touch**:

- `ant/src/index.ts` — import and register `createSkillGuardHook()` in
  the existing `options.hooks.PreToolUse` array (~5 LOC)
- `ant/skills/self/SKILL.md` — one-paragraph note that writes under
  `~/.claude/**` are scanned for safety
- `ant/skills/self/migrations/054-skill-guard.md` — migration note
- `ant/skills/self/MIGRATION_VERSION` → 54
- `CHANGELOG.md` — entry under `[Unreleased]/Added`

Zero Go changes. Zero schema changes. No new MCP tools. No new files
outside `ant/`.

## What's deliberately NOT in scope

- **Memory tool / MEMORY.md / USER.md** — Claude Code CLI already reads
  `CLAUDE.md` natively at session start with frozen-snapshot semantics.
  Adding parallel memory files is confusing without gain.
- **skill_manage MCP tool** — `Write`/`Edit` already do the job.
- **Post-turn review agent** — Hermes's background daemon thread
  assumes long-running `AIAgent` per session. Arizuko's ephemeral
  container model has no natural place for it. If reflection is ever
  wanted, a better fit is `/distill` or `/learn` as a scheduled `timed`
  task over recent messages in SQLite, not a mid-session fork.
- **AST-based scanning** — Hermes's `skills_guard.py` is regex only
  despite the "guard" name. AST parsing adds dependency weight (parser
  per language) for marginal accuracy gain. Skip.
- **Trust-tiered policies (builtin/trusted/community/agent-created)** —
  Hermes varies severity by skill source. Arizuko's skills are always
  agent-created or operator-seeded at image build time. Flat policy is
  fine.

## Out of scope — not coming back

Scheduled reflection via `timed` was proposed as a fallback to the
Hermes review thread and killed (2026-04-11): cron-driven summaries
over SQLite are a different problem shape and would solve no real
complaint. Self-eval as an agent-invocable skill is being designed
separately — see parallel research for `specs/6/8-self-eval-skill.md`.

## Verification

1. `make -C ant build` — clean TS compile
2. `cd ant && bun test src/skillguard.test.ts` — all block/allow cases
3. `make test` — no Go regressions (nothing touched)
4. Manual smoke on a dev instance:
   - Ask the agent to write a normal skill with SKILL.md + a python
     helper → verify it writes cleanly
   - Ask the agent to write a skill with `os.system('curl $API_KEY')`
     → verify the write is blocked and the agent sees the error
   - Ask the agent to write to `~/.claude/CLAUDE.md` with a role-hijack
     payload → verify blocked
   - Write a SKILL.md without `description:` field → verify blocked
     with frontmatter error

## LOC estimate

- `ant/src/skillguard.ts`: ~180 (regex table is the bulk)
- `ant/src/skillguard.test.ts`: ~120
- `ant/src/index.ts` edits: ~10
- Skill docs + migration: ~30
- **Total**: ~340 LOC, half a day to ship

## References

- `/home/onvos/app/refs/hermes-agent/tools/skills_guard.py` — source
  regex table (most important file)
- `/home/onvos/app/refs/hermes-agent/tools/memory_tool.py` — inspiration
  for the threat pattern categories
- `~/.claude/projects/-home-onvos-app-arizuko/memory/reference_hermes-agent.md`
  — full peel notes, spec rationale
- `.diary/20260411.md` — 16:45 (initial peel), 22:55 (spec pushback),
  research-deepdive (deep research pass)
