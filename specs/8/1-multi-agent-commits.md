---
status: deferred
---

# Multi-agent commit coordination

Multiple agents on one repo race on staging + pre-commit + `index.lock`.
Adopt openclaw's committer pattern: a `committer` script that does
`git restore --staged :/` → scoped `git add -- <files>` → scoped
`git commit -m <msg> -- <files>`; blocks `.` and `node_modules`;
validates files exist; retries on stale `index.lock` with `--force`.

Ship locations (precedence repo-local > container skill > global):

- `scripts/committer` repo-local
- `ant/skills/commit/committer` container skill (seeded to `~/.claude/`)
- `~/.claude/scripts/committer` for dev sessions

Rationale: skill instructions are soft; a wrapper script is hard to
misinterpret and handles lock contention uniformly.

Unblockers: ship the script + skill revision saying "use committer if
present, else manual steps." Typecheck scope (`pass_filenames: false`
means it runs on all `.ts` regardless of staging) needs a separate
answer.

Out of scope: full openclaw 24-type plugin hook system; arizuko
already has PreCompact/Stop/PreToolUse.
