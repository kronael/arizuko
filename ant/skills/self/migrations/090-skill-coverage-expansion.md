# Migration 090 — skill coverage expansion + 0043 typed-JID tail

Two threads landed together:

## (a) Six new reference skills

Audit-driven (`.ship/skill-audit-2026-05-02.md`): the capabilities
shipped in 086–089 (OAuth linking, group-state removal, typed JIDs,
`send_voice`) had no agent-side discovery surface. Six new skills
each <80 lines, frontmatter with single-sentence description for
`/dispatch` discovery:

| skill        | use when                                                |
| ------------ | ------------------------------------------------------- |
| `voice`      | deciding whether to send voice; checking platform support |
| `channels`   | dispatching files when adapter capability differs       |
| `mcp`        | calling MCP tools from bash/python/go scripts via mcpc  |
| `auth`       | explaining OAuth linking, recovering from collision     |
| `slink-mcp`  | external agents driving a group via the slink endpoint  |
| `typed-jids` | full type matrix; `room=` vs `chat_jid=` predicates     |

No new tools, configs, or DB columns. Skill content only.

## (b) Howto template expansion + cookbooks page

`web/template/pub/howto/CONTENT.md` grew sections covering voice,
file format support per adapter, typed JIDs, OAuth linking, slink
MCP, channels & routing. Inline edits to existing sections add
`get_thread` alongside `fetch_history` and call out scheduled-task
isolation.

`web/template/pub/cookbooks/CONTENT.md` is new — 7 scenario recipes
(daily voice digest, multi-agent slink-MCP handoff, adaptive file
delivery, account linking onboarding, thread-scoped recall,
isolated cron). Rendering this page is left to a future skill —
the existing `howto` skill hard-reads the howto template only.

## (c) Stale skills updated

- `self` — `send_voice` / `get_thread` / `fetch_history` added to
  the MCP tool table; bare-id JID examples replaced with typed
  forms.
- `compact-memories` — point to `get_thread` for thread-scoped
  memory slices.
- `recall-messages` — protocol step 1 picks between
  `inspect_messages` (whole chat), `get_thread` (one topic), and
  `fetch_history` (platform backfill).

## (d) Operational fix on the host: `0043-typed-jids-tail.sql`

`0042` left three columns un-rewritten: `routes.match` `room=`
predicates, `scheduled_tasks.chat_jid`, and `chat_reply_state.jid`.
On any instance that ran `0042`, every routed telegram chat became
unrouted (`JidRoom("telegram:user/X")` returns `"user/X"` but the
route still matched the bare ID). The gateway then ran
`InsertOnboarding` on each inbound and `onbod` re-prompted
operational chats with auth links. `0043` rewrites all three
columns idempotently and ships in this image.

## What you need to do

Nothing. Skills propagate via the rebuilt agent image; new
reference skills are auto-discoverable through `/dispatch`.

If you previously hand-wrote a routing rule using a bare-ID
`room=<digits>` predicate, `0043` rewrote it for you. New routes
created by the dashboard or `add_route` MCP already use the typed
form (the code calls `JidRoom` on a typed JID, producing
`user/<id>` / `group/<id>`).
