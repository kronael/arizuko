# Storage — persistent vs transient

`~` (= `/home/node/`) is your group workspace. Persists across
container restarts and sessions. Write anything here that should
survive.

## Agent home is your kingdom (v0.45.11+)

> Two web slots, both bind-mounted from the unified web tree:
>
> - **`~/public_html/`** → served at `/pub/<your-folder>/...` (no auth)
> - **`~/private_html/`** → served at `/priv/<your-folder>/...` (OAuth/JWT)
>
> Off-web storage (`~/workspace/`, `~/diary/`, `~/facts/`, `~/users/`,
> `~/.claude/`) is never served at any URL. Truly private content
> stays here.
>
> Read-only browse of the whole public web tree at `/var/lib/www/`.

## What goes where

| Path              | What to put there                                          | URL?                                  |
| ----------------- | ---------------------------------------------------------- | ------------------------------------- |
| `~/diary/`        | Session diary entries (use `/diary` skill)                 | no                                    |
| `~/facts/`        | Researched reference facts (use `/find`)                   | no                                    |
| `~/users/`        | Per-user memory (use `/users`)                             | no                                    |
| `~/.claude/skills/` | Custom skills you create or install                      | no                                    |
| `~/workspace/`    | Long-lived project files, code, data                       | no                                    |
| `~/tmp/`          | Single-run scratch — survives but treat as disposable      | no                                    |
| `~/public_html/`  | Public web content                                         | `/pub/<your-folder>/...` (no auth)    |
| `~/private_html/` | OAuth-gated web content                                    | `/priv/<your-folder>/...` (JWT)       |

## Two URLs, one file

`/pub/<X>` (public) and `/<X>` (JWT-rewrite) serve the SAME file from
`<data>/web/pub/<X>`. `/priv/<X>` serves a DIFFERENT file from
`<data>/web/priv/<X>` — a separate filesystem tree, never reachable
via `/pub/`.

## Containers are ephemeral

A fresh container starts for each agent run. `~/` is volume-mounted so
it persists. Anything written OUTSIDE `~/` (e.g. `/tmp/`) is lost when
the container exits. NEVER store run outputs in `/tmp/`.
