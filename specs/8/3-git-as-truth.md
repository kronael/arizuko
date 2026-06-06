---
status: drafting
depends: specs/5/5-uniform-mcp-rest.md, specs/7/2-data-model.md
---

# specs/7/3 — git as the source of truth

## Why

Three motivations stack:

1. **Audit, history, fork, distribute** — git already ships all of
   these as native verbs. Reinventing them inside a database
   (bespoke event log, audit table, fork machinery, distribution
   protocol) is the classic mistake. Git is the most-known
   versioned-data primitive on the planet.
2. **The platform thesis** (`specs/7/index.md`) — "agent is data."
   Data deserves the versioning model that values get: commits,
   branches, signatures, remotes. SQLite is a CRUD store; CRUD is
   the wrong model for _configuration of agents that operate other
   agents_.
3. **Forking + distribution** — a customer's "oncall agent" should
   be a thing you can clone, copy, branch, push, pull. Today it is
   a folder under `/srv/data/...` with a DB row referencing it.
   Under git, it is a commit on a branch in a repo. Same primitive
   the developer ecosystem already understands.

## What this spec is

The mechanism. Where the bits live, who writes them, how the cache
stays in sync, what crash recovery looks like, what fork costs,
what audit costs.

## The architecture (3 layers)

```
┌──────────────────────────────────────────────────────────────┐
│  GIT TREE  (source of truth — versioned, signed, distributed)│
│  /                                                            │
│    agents.toml                products + deployments         │
│    acl/                       per-scope rule files           │
│    routes/                    route table (ordered TOML)     │
│    skills/                    referenced by products         │
│    personas/                  referenced by products         │
│    (NO secrets/ in git — refs only, blobs stay in SQLite)    │
│    groups/<folder>/                                          │
│      MEMORY.md                                               │
│      .diary/                                                 │
│      PERSONA.md               (if folder-overridden)         │
│      turns/<YYYY-MM-DD>.jl    per-day turn digest            │
│      decisions/<sha>.md       per-turn decision sidecar      │
└──────────────────────────────────────────────────────────────┘
                          ↓ derive ↓
┌──────────────────────────────────────────────────────────────┐
│  SQLITE  (operational cache — rebuildable from git+input log)│
│    messages queue + cursors                                  │
│    indexes (fast routing/dispatch)                           │
│    in-flight turn state                                      │
│    materialized projections (graph view)                     │
└──────────────────────────────────────────────────────────────┘
                          ↓ projects ↓
┌──────────────────────────────────────────────────────────────┐
│  CONTAINERS  (ephemeral — projection of git@HEAD into runtime│
└──────────────────────────────────────────────────────────────┘
```

## Who writes git

**The gateway is the only git writer.** Agents never `git commit`
directly. Every state-changing MCP/REST call goes through the
unified handler (Action 1).

**For cold things (in git): writes are synchronous, directly to
the working tree.** No SQLite shadow. Reads also come from the
working tree (or a cheap in-memory cache rebuilt from the tree on
startup). The cold tier IS git; it does not live in two places.

**For warm/hot things (not in git): writes go to SQLite as today.**

**Commits are batched at turn boundary.** During a turn, mutating
calls stage files via `git add`. At turn end, the gateway writes
the `decisions/<turn_sha>.md` sidecar and runs `git commit -m
"<summary>"` once. Multiple stages → one commit per turn.

This avoids:

- Dual-write inconsistency for cold tier (only one writer, only
  one place).
- Crash-recovery complexity for cold tier (working tree IS truth;
  uncommitted staged files survive as the staging area).
- Commit floods on busy turns (one commit per turn, not per event).

## Commit granularity

**One commit per turn boundary.** The commit message is a short
summary; the `decisions/<sha>.md` sidecar references the row range
in `audit_log` for that turn (`audit_log.id BETWEEN <turn_start_id>
AND <turn_end_id>`) — that table is the canonical record of every
state-changing call ([`../6/F-audit-stream.md`](../6/F-audit-stream.md),
field schema [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md)).
The sidecar carries the human summary; `audit_log` carries the
machine-readable rows. Sub-turn lineage lives in the table; turn-level
lineage lives in the git history.

Why not per-event commits: a busy turn can emit dozens of MCP
calls. Per-event would produce dozens of commits per turn,
bloating history and making `git log --oneline` useless.

Why not per-day commits (digests only): a single bad turn buries
inside a day's roll-up; bisecting becomes impossible.

Why not journalctl as the audit substrate: slog → journald is lossy
by design (rotation, level filtering). The sidecar needs a stable
key that survives forever — `audit_log.id` is that key.

## Hot path: inbound messages

Inbound messages are high-volume (a busy Slack group hits hundreds
of msgs/day). Three options:

1. **Per-message commit** — out of scope; would crush git.
2. **Per-day digest commit** — at midnight UTC per folder, dump
   `messages` since last cursor into `turns/<YYYY-MM-DD>.jl` and
   commit. Single commit per day per folder.
3. **Hot-only in SQLite** — never committed; messages are
   ephemeral, only decisions persist.

**Lean (2).** Per-day digest in git, hot copy in SQLite for
operational queries. The digest commit lets you `git checkout` a
date and see what was happening on the bus. Open question: do we
need it, or is (3) enough? Decide during implementation by usage.

## Crash recovery

Because cold tier is direct-to-git (no SQLite shadow), there is
nothing to reconcile for cold writes. The working tree IS truth.
On crash:

- **Mid-turn (before commit):** uncommitted staged changes survive
  in the git index. Gateway restart finds them and either commits
  with a `[recovery]` prefix (if the turn was complete) or
  discards via `git restore --staged .` (if the turn was aborted).
  Heuristic: presence of the sidecar file `decisions/<turn_sha>.md`
  marks turn completion.
- **Mid-commit:** git's own commit is atomic. Either the commit
  lands or the index is preserved. Re-run on next start.

No `audit_log committed_at` machinery is needed for the cold tier —
the git index IS the staging area. The warm `audit_log` table
([`../6/F`](../6/F-audit-stream.md)) is its own crash-safe substrate
(SQLite WAL); rows are transactional with their mutations. The
turn-end sidecar references the row range that landed during the
turn, so a `[recovery]` commit can be reconstructed from the table
even if the in-flight Go state was lost.

## Secrets are NOT in git

Secrets stay in SQLite, AES-256-GCM encrypted, as today
(`store/secrets.go`). Git carries only references
(`{ scope = "folder", name = "slack" }`) — no values, no
ciphertext. Per `specs/7/2-data-model.md`.

Rationale: git is for _configuration that should be distributed,
forked, audited_. Secret blobs are _operational state that must
stay on the operator's host_. Pushing encrypted-but-distributable
blobs invites compromise (one stolen key decrypts the whole
history). Operator vault → in-host SQLite is the trust boundary;
git crosses it for config, never for secrets.

Rotation, BYOA layering, per-call audit — all stay in
`store/secrets.go`, in **Phase C of `specs/5/32-tenant-self-service.md`**
(folder/user-scope secrets layering), and in
`specs/6/Y-secret-broker.md` (tool-level audit edge). No
phase-7 work here.

## Federation topology

**One repo per instance** (krons, marinade, sloth). Products
defined in _shared product repos_, included via submodule or
`arizuko product fetch <url>`. Same shape as Terraform modules.

Cross-instance products: the product is in its own repo; multiple
instance repos reference it. Updates to the product propagate via
`arizuko product update <name>` on each instance.

Open: monorepo for organizations running multiple instances. Likely
fine as a deployment choice; not a platform feature.

## Fork lifecycle

`arizuko fork <folder> --at <commit>`:

1. `git worktree add <new-path> <new-branch>` from `<commit>`.
2. Spawn a fresh container against the worktree (NOT the main
   working tree).
3. New folder gets a fresh SQLite cache, rebuilt from the worktree
   state.
4. Merge-back is operator's call (`git merge`, conflict
   resolution). Until merged, the fork is parallel reality with no
   visibility to the main folder.

Cost: one worktree + one container + one SQLite cache. Cheap.

## Operator UX

Wrap git for the common cases — most operators won't `git commit`
directly:

| `arizuko log <folder>` | `git log` filtered to that folder's path |
| `arizuko diff <a>..<b>` | `git diff` between commits or folders |
| `arizuko revert <commit>` | `git revert` + reconcile (applies cache reset) |
| `arizuko apply` | reconcile DB cache with current git HEAD |
| `arizuko plan` | dry-run apply — show the diff |

Direct git is still allowed for advanced operators (project rule:
"strict, not magical").

## dashd tier-1 write surface

Today edits SQLite directly. Under git-as-truth:

**Option A (commit then apply):** dashd commits to git, then runs
`arizuko apply` immediately on the same instance. Operator sees
the change reflected.

**Option B (commit + auto-apply):** dashd's write goes straight
through the unified handler (Action 1), which writes SQLite first

- stages git changes; commit happens at next turn boundary or via
  a digest timer.

**Lean B.** Same code path as agent-initiated mutations. Operator
mutation IS a turn (zero-LLM, but same shape). Keeps the
write-graph singular.

## Performance ceiling

Numbers:

- 50 folders × 365 days = 18.25K commits/year for digest commits.
- Linux kernel does ~70K commits/year, single repo, no issues.
- Per-turn commits add: avg 5 turns/day/folder × 50 folders × 365 =
  91K commits/year. Combined with digests: ~110K commits/year.
- Git handles this comfortably up to ~1M commits in a single repo
  before `git log` starts feeling slow. We're an order of
  magnitude under.

Danger zones:

- Per-message commits → don't (use digests).
- Bulk media in git → use git-LFS or hash-references in commits,
  blobs outside-tree.
- Single-repo monorepo with thousands of folders → compaction
  concerns; split repos before that.

## Migration plan — pragmatic, easy-first

Principle (per user, 2026-05-23): **put in git what we can put in
git easily now. Postpone the hard parts.**

Phased migration, easy entities first:

1. Land Action 1 (MCP+REST unification) — single mutation path.
2. Land Action 2 (data model sharpening) — entities know their
   tier.
3. **Phase 3a (easy)** — migrate purely-cold entities that already
   have a clear file shape:
   - `acl/` rules (already file-friendly)
   - `routes/` table → ordered TOML
   - `personas/` and `skills/` references (already files on disk;
     just commit them to the per-instance repo)
   - `MEMORY.md` and `.diary/` per folder (already files; just
     commit)
     These move first because the serialization is obvious and the
     write volume is low.
4. **Phase 3b (medium)** — `products` and `deployments` as
   `agents.toml` (new entities; born in git, no migration from
   SQLite).
5. **Phase 3c (postpone)** — entities where the git shape is
   non-obvious or where the operational coupling is tight:
   - `secrets` — decide blob storage first (see open questions).
   - `chats` — split needed (see `specs/7/2-data-model.md`); keep
     in SQLite until split is clean.
   - Per-day inbound digest commits — only if usage proves the need;
     SQLite-hot-only is acceptable indefinitely.
6. Provide `arizuko rebuild-cache` verb for one-shot recovery.

Migration is per-instance, operator-controlled. No global cutover.
Anything in Phase 3c stays in SQLite until its specific spec lands.

## Non-goals

- Replacing SQLite. Cache stays.
- Implementing distributed git or multi-master. One writer per
  instance.
- Cross-instance live sync. That's federation via `git remote`,
  not real-time.
- A bespoke event log. Use git history.

## Acceptance

- `arizuko apply` reconciles DB cache to git HEAD; idempotent.
- `arizuko fork <folder> --at <sha>` works end-to-end (worktree +
  container + cache rebuild).
- Per-turn commits visible in `git log`; sidecar metadata parses.
- Crash recovery test: kill gateway mid-turn, restart, verify
  `[recovery]` commit lands and cache matches git.
- `make test -short` passes; new integration tests cover the
  gateway committer.

## Open questions

- Inbound message storage (per-day digest vs hot-only) — see above.
- Sidecar enough or per-event commits on a side branch — see above.
- `arizuko log/diff/revert` wrapper UX — what surface do operators
  actually want?
- "GitOps" as a term vs "git-native" — affects positioning, not
  mechanism. Decide before POSITIONING.md ships.

## Pointers

- ActiveGraph honesty check: `.diary/20260523.md`, ~15:00 block.
- Three-lens synthesis (agent-is-data, agent-first, agent-is-graph
  scoped to organizational layer): same diary block.
- Prior art: Argo CD, Flux, Atlantis — all do GitOps for K8s. The
  shape is well-trodden; we are doing GitOps for agent fleets.
