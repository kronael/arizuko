---
status: shipped
depends: [B-route-mode-ingestion]
relates-to:
  [4/23-topic-routing, 3/Y-thread-routing, 3/a-sticky-routing, G-engagement]
revision: 6
---

# specs/5/F — topic lineage: forks + per-topic observed cursor

Two orthogonal features on the topic primitive.

## Fork = plain `cp` of the session jsonl, nothing else

Claude Code stores each session as a jsonl under
`<groupDir>/.claude/projects/-home-node/<uuid>.jsonl`. Forking
`(folder, parent)` into `(folder, child)` copies that file to a new uuid
and writes a `sessions` row with `parent_topic`, `forked_at` and
`observed_cursor=now`. The child runs `--resume <child_uuid>` and
continues; every prior turn IS in its history.

**No marker line, no history rewrite, no injected `<inherited>` block.**
The parent's tail is always a valid resume point — the parent ran fine
when it was forked. The rejected alternative was rendering an inherited
history window into the child's first prompt: it duplicated context the
resume already carried, cost a `TopicHistoryThrough` reader and two env
knobs, and had no way to be correct for fork-of-fork.

**The slug is fixed (`-home-node`)** because every container mounts
`groupDir` at `$HOME=/home/node`, so Claude Code slugifies the path
identically across folders. Deriving it per folder would be the same
identity-from-path mistake CLAUDE.md bans.

**cp is rename-after-write** (`container/runner.go:1132` `CopySession`) —
a crash mid-copy must not leave a partial child session. **A missing
parent file is a WARN, not an error**: the child starts fresh with no
parent context, which is degraded but correct; failing the turn would
not be.

Trigger sites, all one function
(`routd/dispatch.go:380` `ensureTopicWithFork` →
`:398` `copyParentSession`; the row insert is
`store/sessions.go:107` `ForkTopic` / `EnsureTopicLineage`, which does
**not** itself copy the file):

1. Explicit MCP `fork_topic(parent, child, force=false)` — `force=false`
   on an existing child returns `ErrTopicExists`.
2. First agent turn for any non-main topic forks the folder's main
   session.
3. When the trigger message has a `reply_to_id`, the parent is
   `TopicByMessageID(reply_to_id)` rather than main.

Only the parent argument differs.

## The agent's scope awareness is the `<topic>` envelope

`buildAgentPrompt` emits `<topic name="…" />` every turn. That is the
whole mechanism. **The agent is never told it was forked** — the relevant
question is "what topic am I in NOW", which is the same whether the
session was forked or started fresh. `parent_topic` and `forked_at` are
retained for audit; the prompt path does not read them.

`ant/CLAUDE.md` carries one rule: replies stay scoped to the turn's
topic; to switch, say so and call `fork_topic` or use `#topic` syntax.

## Per-topic observed cursor

`sessions.observed_cursor` (RFC3339Nano UTC).
`ObservedSince(folder, cursor, maxMsgs, maxChars)`
(`store/messages.go:397`) reads rows strictly after the cursor; routd
advances it after rendering.

**The cursor is per-`(folder, topic)`, not per-folder.** That fixed a
live bug: topic A consumed the folder's observed window and topic B in
the same folder never saw those messages at all.

At-least-once on crash recovery — the advance is not transactional with
the run. The agent's standing "observed messages are context, not
requests" rule absorbs the duplicates.

## `observe_group` — directional cross-folder ambient

`observe_group(source)` / `unobserve_group(source)` (`ipc/ipc.go:1681`)
make the calling folder receive `source`'s messages as `<observed>`
context on its next trigger turn. The observer does not become source's
agent; it gets a read-only feed. Storage: `group_watchers
(observer, source)`; `ObservedSince` UNIONs watched sources' rows into
the ambient query (`store/messages.go:403`).

**Distinct from `set_group_open`**, which exposes _already-observed_
(`is_observed=1`) rows to open siblings and therefore requires an
`#observe` route on the source side to produce them. `observe_group`
picks up source's _primary-delivered_ (`is_observed=0`) messages, so it
needs no route change on the source at all. Two different questions —
"who may see my ambient" vs "whose primary traffic do I want ambient" —
so two mechanisms, not one flag.

Authorization is `authzStructural` (`ipc/ipc.go:931`) — the target must
fall inside the caller's granted scope, evaluated by the single ACL
evaluator ([`32-acl-unified.md`](32-acl-unified.md)). The tier table an
earlier revision carried is gone.

## What this is NOT

- **NOT a marker-line-in-history hack.** No synthetic entries in the
  session jsonl.
- **NOT crash-safe-atomic cursor advance.** At-least-once by design.
- **NOT cross-runtime.** Fork is Claude-Code-specific (jsonl file shape);
  another agent runtime needs its own equivalent.
- **NOT a recursive rewrite on fork-of-fork.** Each fork is one cp from
  its immediate parent; a grandchild is historically two cps. Session
  files duplicate storage per fork — accepted; GC of old child sessions
  is future work.
