---
status: shipped
depends: [B-route-mode-ingestion]
relates-to:
  [4/23-topic-routing, 3/Y-thread-routing, 3/a-sticky-routing, G-engagement]
revision: 6
---

# specs/5/F — topic lineage: forks + per-topic observed cursor

## What this is

Two orthogonal features on the topic primitive:

1. **Fork** = plain `cp` of parent's Claude Code session jsonl to a
   new uuid. Child resumes natively from parent's tail.
2. **Per-topic observed cursor** = each topic tracks its own
   watermark over `is_observed=1` messages so two topics in the
   same folder both see the same ambient context.

No inserted history blocks. No special prompt injection at every
turn. The agent learns what scope it's in from the per-turn
`<topic>` envelope (spec 5/X's `<surface>` family).

## Fork — plain cp, nothing else

Claude Code stores each session as a jsonl at
`~/.claude/projects/<slug>/<uuid>.jsonl`. To fork (folder, parent)
into (folder, child):

```
child_uuid  = NewSessionID()
parent_path = ~/.claude/projects/<slug>/<parent_uuid>.jsonl
child_path  = ~/.claude/projects/<slug>/<child_uuid>.jsonl

cp parent_path child_path

sessions[(folder, child_topic)] = (
  session_id      = child_uuid,
  parent_topic    = parent,
  forked_at       = now,
  observed_cursor = now,
)
```

No marker line, no rewrite. Parent's tail is always a valid
resume point (the parent ran fine when forked). Child runs with
`--resume <child_uuid>` and continues — every prior turn IS in
the child's history.

## How the agent knows what scope it's in

Per-turn `<topic name="…" />` envelope, emitted by `buildAgentPrompt`
on every prompt (spec 5/X's `<surface>` hint family). The agent
sees on each turn:

```xml
<topic name="#deploy" />        <!-- non-main topic -->
<topic name="" />                <!-- main -->
<surface>slack-channel-thread</surface>
…trigger messages…
```

That's the entire scope-awareness mechanism. The agent doesn't need
to know it was forked from another topic — the relevant info is
"what topic am I in NOW", which is the same whether the session
was forked from main or started fresh.

### ant/CLAUDE.md rule

One line:

> Every turn carries `<topic name="X" />`. Replies stay scoped to
> that topic. If switching topics is needed, say so and call
> `fork_topic` or use `#topic` syntax — don't conflate across
> topic boundaries.

## When forks happen

Three triggers, all map to `ForkTopic(folder, parent_topic, child_topic)`:

1. **Explicit MCP** — `fork_topic(parent, child, force=false)`.
2. **Default-fork-from-main** — first agent turn for any non-main
   topic forks the folder's main session.
3. **Reply-to-parent** — when trigger message has `reply_to_id`,
   parent = `TopicByMessageID(reply_to_id)` instead of main.

The function is one; only the parent argument differs.

## Per-topic observed cursor

`sessions.observed_cursor` (RFC3339Nano UTC). `ObservedSince(folder,
cursor, maxMsgs, maxChars)` reads `is_observed=1` rows strictly
after cursor. Gateway advances cursor after rendering. At-least-once
on crash recovery; the agent's existing "observed are context, not
requests" rule handles dupes.

Fixes the live bug where topic A consumed the observed window and
topic B in the same folder never saw it.

## Schema (already shipped — no change in this rev)

```sql
-- 0055-topic-lineage.sql (already in HEAD)
ALTER TABLE sessions ADD COLUMN parent_topic    TEXT;
ALTER TABLE sessions ADD COLUMN forked_at       TEXT;
ALTER TABLE sessions ADD COLUMN observed_cursor TEXT;
CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);
```

Lineage columns retained for audit / future use; the prompt path
no longer reads them. `parent_topic` and `forked_at` are now
metadata only.

## Code surface

| File                  | Change                                                                                                                                                                 | LOC  |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| `container/runner.go` | `CopySession(srcUUID, dstUUID, folder)` — pure cp                                                                                                                      | ~30  |
| `store/sessions.go`   | `ForkTopic` calls `CopySession` after insert                                                                                                                           | ~5   |
| `gateway/gateway.go`  | `EnsureTopicLineage` callsite triggers cp when parent session exists; **remove** `<inherited>` block rendering in `buildAgentPrompt`; emit `<topic>` envelope per turn | ~−30 |
| `store/messages.go`   | **remove** `TopicHistoryThrough` (unused after this rev)                                                                                                               | ~−45 |
| `core/config.go`      | **remove** `InheritWindowMessages`/`InheritWindowChars` env vars (unused)                                                                                              | ~−10 |
| `ant/CLAUDE.md`       | one-line `<topic>` envelope rule                                                                                                                                       | 3    |
| Tests                 | full coverage (see below)                                                                                                                                              | ~150 |

**Net: ~+100 LOC including tests.** Production code shrinks by
~80 LOC vs the inherited-block implementation just removed.

## Tests required

Comprehensive — operator explicitly called out new-session-in-thread

- new-session-in-topic.

### Fork unit

- `ForkTopic("main", "#deploy")` — sessions row written with
  parent_topic="" / forked_at=now / observed_cursor=now.
- Child file exists at `~/.claude/projects/<slug>/<child>.jsonl`
  and is byte-identical to parent.
- `ForkTopic` with force=false on existing child → `ErrTopicExists`.
- `ForkTopic` with force=true on existing child → overwrites.
- `CopySession` when parent file doesn't exist → returns error;
  caller surfaces graceful fallback (log WARN, child starts fresh).

### Session-starts-in-topic — integration

- Send message with `#deploy` topic prefix in a folder where
  `#deploy` doesn't exist yet.
- Verify: `sessions` row created with parent_topic="", session file
  forked from main's session file.
- Verify: agent prompt for first `#deploy` turn carries
  `<topic name="#deploy" />`.
- Verify: agent receives full main history via `--resume`, not
  via inherited block (assert no `<inherited>` substring in prompt).

### Session-starts-in-thread — integration

- Send message in a Slack channel; bot replies (creates `chats_reply_state`).
- User replies in thread to bot's message — `topic=<thread_ts>`,
  `reply_to_id` set.
- Verify: `sessions` row for new thread topic created with
  parent_topic = main's topic (because reply was to a main-topic message).
- Verify: session file forked from main's session.
- Verify: thread reply triggers because `chat_reply_state.engaged_until`
  is active (engagement, spec 5/G).
- Verify: prompt carries `<topic name="<thread_ts>" />` +
  `<surface>slack-channel-thread</surface>`.

### Reply-to-non-main

- In folder, create `#deploy` topic (forks main).
- Send message in `#deploy`; bot replies.
- User opens thread on bot's `#deploy` reply.
- Verify: new thread topic forks from `#deploy`, not from main.
- Verify: session file copy preserves `#deploy`'s history.

### Per-topic observed cursor

- Create one observed message in folder.
- Topic A reads via `ObservedSince(folder, "", …)` — sees it,
  advances cursor.
- Topic B (fresh, empty cursor) — still sees it.
- Topic A reads again with its advanced cursor — sees nothing.

### `<topic>` envelope rendering

- Trigger turn for topic `#deploy` → prompt contains
  `<topic name="#deploy" />`.
- Trigger turn for main → prompt contains `<topic name="" />`.
- Verify envelope appears before trigger messages, after sysMsgs.

## observe_group — directional cross-folder subscription

`observe_group(source)` makes the calling folder (observer) receive
source's inbound messages as `<observed>` context on the observer's
next trigger turn. The observer does not become source's active agent;
it just gets a read-only ambient feed.

**Distinction from `set_group_open`**: `set_group_open` exposes
_already-observed_ (is_observed=1) messages to open siblings. It
requires an `#observe` route on the source side to produce those rows.
`observe_group` is directional and picks up source's _primary-delivered_
(is_observed=0) messages — no route change needed on source.

### Implementation

New table `group_watchers (observer TEXT, source TEXT, PRIMARY KEY
(observer, source))`. `ObservedSince` UNION-joins watched source folders'
primary messages (is_observed=0) alongside the existing is_observed=1
query.

### Auth

Tier 0/1: any target. Tier 2: own subtree or parent chain (escalation).
Tier 3: denied.

### Schema

```sql
-- 0063-group-watchers.sql
CREATE TABLE IF NOT EXISTS group_watchers (
    observer TEXT NOT NULL,
    source   TEXT NOT NULL,
    PRIMARY KEY (observer, source)
);
```

### MCP tools

- `observe_group(source)` — subscribe
- `unobserve_group(source)` — cancel

## What this is NOT

- **NOT a marker-line-in-history hack.** No synthetic entries
  appended to the session jsonl. Plain cp only. The agent's
  scope awareness comes from the per-turn `<topic>` envelope.
- **NOT crash-safe-atomic cursor advance.** At-least-once;
  agent rule handles dupes.
- **NOT cross-runtime.** Fork is Claude-Code-specific (jsonl
  file shape). Other agent runtimes need their own equivalent.
- **NOT recursive history rewrite on fork-of-fork.** Each fork
  is one cp from the immediate parent. Grand-child gets a chain
  of two cp operations historically — no special handling.

## Open questions

- **Container vs host file paths**: arizuko spawns agents in
  containers; `~/.claude/projects/` resolves to a mounted host
  path. `CopySession` must operate on the host path that gated
  can write to, with the same path the in-container agent reads.
  Verify via container.GroupHome or similar plumbing. Spike before
  shipping.
- **`cp` atomicity**: SQLite WAL guarantees the sessions row insert
  is atomic. The cp is not — if process dies mid-cp, child file is
  partial. Use rename-after-cp pattern (`cp parent tmp; mv tmp child`)
  to make cp effectively atomic.
- **Session file size**: long-running parents have multi-MB sessions.
  cp duplicates storage per fork. Acceptable; long-term GC can
  prune old child sessions.
