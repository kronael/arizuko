# 004 — send_file is mandatory for file delivery

Updated CLAUDE.md to enforce that files must be delivered via `send_file`,
never described or inlined in text responses.

## What changed

- `container/CLAUDE.md` now has a "Delivering files to users" section
  that states ALWAYS use `send_file`, NEVER inline file contents.

## Agent action required

The updated CLAUDE.md will be seeded to `~/.claude/CLAUDE.md` on next
`/migrate` run. No other changes needed.
