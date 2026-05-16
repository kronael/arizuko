# Storage — persistent vs transient

`/home/node/` is your group workspace. Persists across container restarts
and sessions. Write anything here that should survive.

| Path | What to put there |
| ---- | ----------------- |
| `/home/node/diary/` | Session diary entries (use `/diary` skill) |
| `/home/node/facts/` | Researched reference facts (use `/find`) |
| `/home/node/users/` | Per-user memory (use `/users`) |
| `/home/node/.claude/skills/` | Custom skills you create or install |
| `/home/node/workspace/` | Long-lived project files, code, data |
| `/home/node/tmp/` | Single-run scratch — survives this session but treat as disposable |

`/workspace/web/pub/` is served publicly and persists (separate mount).

Containers are **ephemeral per turn** — a fresh container starts for each
agent run. `/home/node/` is volume-mounted so it persists; anything written
OUTSIDE `/home/node/` or `/workspace/` (e.g. `/tmp/`) is lost when the
container exits. NEVER store run outputs in `/tmp/`.
