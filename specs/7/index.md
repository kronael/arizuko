---
status: drafting
---

# specs/7 — platform program: MCP+REST unification, data model, git-as-truth

The platform thesis crystallized in the 2026-05-23 framing session.
This phase carries it from positioning to mechanism.

## The framing (one paragraph)

arizuko is an **agentic product platform** with two surfaces and one
discipline. The surfaces: **MCP+REST** (the operations + runtime
protocol, agent-first, REST as impedance match) and **git** (the
serialized representation, the audit trail, the fork primitive, the
distribution channel). The discipline: **agent is data** — persona,
skills, ACL, routes, products, secrets-pointers, decisions, memory,
diary are all values, versioned in git, mutated through the
MCP+REST gate, projected into running containers at HEAD. Like
"code is data" became cloud and infrastructure-as-code, now we have
_agents as data_ — and agents managing agents on top of it.

## Why this is a phase, not a feature

Each of the three actions is independently shippable. Together they
are the platform thesis. Out of order they don't compose. Pre-ordered:

1. **MCP+REST unification** — finish what `specs/6/5-uniform-mcp-rest.md`
   started. One hand-rolled handler per resource, both protocols,
   identical scopes + auth gate. Without this, the operator + agent
   surfaces drift and the "git as truth" reconcile loop has two
   masters to chase.
2. **Data model improvements** — sharpen the entities (chats, routes,
   grants, secrets, products, deployments) so they can be cleanly
   serialized to git files. Many tables today are operational shapes,
   not authoritative state — that boundary needs explicit lines
   drawn before the git move can be principled.
3. **Git as truth** — move what we can put in git easily _now_
   into a per-instance git repo. Cold tier (ACL, routes, persona,
   skills, MEMORY.md, .diary/) goes synchronously, written direct
   to working tree; commits gate at turn boundary. Hard parts
   (secrets blob location, chats split, inbound digest) stay in
   SQLite until their own sub-specs land. Pragmatic, not
   maximalist. Audit, history, fork, distribute — native git
   verbs for everything that does migrate.

## Design principles (carried)

- **One renderer, many sinks** (CLAUDE.md) — MCP+REST unification IS
  this principle applied to the platform's external surface.
- **Strict, not magical** (CLAUDE.md) — git is strict by construction
  (commit or it didn't happen); no silent fallbacks for missing data,
  no parent-folder inheritance for things the operator didn't write.
- **Boring tech** (CLAUDE.md) — git is the most-known versioned-data
  primitive on the planet; reach for it before inventing event logs,
  CRDTs, or bespoke audit tables.
- **Minimality and orthogonality** (CLAUDE.md) — three concerns, three
  spec files, no cross-mixing. Don't smuggle git semantics into the
  MCP/REST unification spec; don't smuggle data-model changes into
  the git move.

## What this phase is NOT

- Not a runtime rewrite. Containers, channel adapters, daemon shape
  unchanged.
- Not a bespoke event-sourcing system. ActiveGraph
  (arxiv:2605.21997) inspired the direction; git replaces the
  bespoke event log + projection + fork machinery. Adopt the
  discipline, not the runtime.
- Not Kubernetes / multi-node. Single-host operator footprint stays.
- Not a product registry (that's `specs/8/...` / future-phase 8 work).
- Not the `agents.toml` declarative composer (that's drafted
  separately under a Tier-A spec referenced from
  `TODO.md`/positioning; sequenced AFTER this phase lands).

## Sequencing

Action 1 unblocks Action 2 (data model can target the unified
surface). Actions 1+2 unblock Action 3 (clean entities + clean
surface = clean git serialization). Within each action, ship the
smallest viable version first; iterate behind that surface.

Hard dependency on **Phase 6 Phase C** (`specs/5/32-tenant-self-service.md`
folder/user-scope secrets layering) — the BYOA primitive. Without
secrets-as-references-in-git, Action 3 can't ship safely.

## Specs in this phase

- [1-mcp-rest-unification.md](1-mcp-rest-unification.md) — close the
  MCP+REST handler gap.
- [2-data-model.md](2-data-model.md) — entity sharpening,
  serialization-friendly shapes.
- [3-git-as-truth.md](3-git-as-truth.md) — gateway as the only git
  writer; dual-write event-sourcing-lite; SQLite as cache; fork via
  `git worktree`; audit via `git log`.

## Open questions (referenced from each spec)

These are real and unresolved at phase-open. Each child spec carries
its own subset; the index keeps the master list.

1. Inbound message storage — per-day JSONL in git, or hot-only in
   SQLite?
2. Crash recovery between SQLite write and git commit — replay
   protocol?
3. Fork lifecycle — does forked branch get its own container, same
   DB, separate cache?
4. Operator UX — `arizuko log/diff/revert` wrappers vs raw git?
5. dashd tier-1 write — commit→apply, or commit-immediate-apply?
6. Tracing granularity vs audit granularity — sidecar JSON enough,
   or per-event commits on a side branch?
7. Term "GitOps" — adopt (familiar to ops buyers) or substitute
   "git-native" (cleaner for non-K8s audience)?

## Pointers

- Framing session diary: `.diary/20260523.md` (afternoon blocks
  ~14:00–15:30).
- Competitive sweep + ActiveGraph honesty check: same diary entry.
- Three-lens synthesis (agent-is-data, agent-first, agent-is-graph
  scoped to organizational layer): same diary.
- Memory pointers: `~/.claude/projects/-home-onvos-app-arizuko/memory/`
  (no permanent memory file yet; this index doubles as canonical
  framing reference until POSITIONING.md exists at repo root).
