---
status: shipped
shipped: 2026-05-01
extended: 2026-05-27
---

# Message history MCP

Agent-side tools for reading message history. All registered in
`ipc/ipc.go:2011-2170`; REST twins at `GET /v1/messages/{inspect,thread,find}`.

## Four tools, four intents

| Tool                                            | Answers                                             |
| ----------------------------------------------- | --------------------------------------------------- |
| `inspect_messages(chat_jid, limit, before)`     | what did the STORE record for this chat (audit)     |
| `get_thread(chat_jid, topic, limit, before)`    | one thread's slice when a chat fans out into topics |
| `fetch_history(chat_jid, limit, before)`        | platform truth, for context before replying         |
| `find_messages(query, scope?, sender?, since?)` | where was that thing said                           |

Distinct names with sharp descriptions rather than one tool with a
`mode=` param, per the project tool-naming rule — the intents differ, and
each description says which of the other three to reach for instead.
`get_history` is not one of them: it was the deprecated alias
`inspect_messages` replaced, and survives only as an operator tool on
webd's own MCP surface (`webd/mcp.go:120`).

## Decision: SQLite FTS5, no second query language

`find_messages` exposes **FTS5 syntax verbatim** — bare token, `"exact
phrase"`, `a OR b`, `a AND NOT b`, `prefix*`, `NEAR(a b, 5)`. Operators
get the query language SQLite documents; no DSL invented, nothing to
teach twice. Malformed syntax surfaces as `400 invalid query`. `:query`
is a bound parameter; FTS5 parses its own syntax inside, so there is no
SQL escaping to get wrong.

**The FTS shadow keys on the implicit `rowid`, never on `id`.**
`messages.id` is `TEXT PRIMARY KEY` (UUID-shaped) and FTS5 external
content requires an INTEGER rowid. Every non-`WITHOUT ROWID` table has
an implicit `rowid` alongside its TEXT PK — that is the join key
(`m.rowid = f.rowid`). Attempting `id` as the FTS rowid is the trap this
line exists to prevent.

Table + triggers: `store/migrations/0070-messages-fts.sql`;
reader `store/messages.go` `FindMessages`. `snippet()` returns the
matched fragment with FTS5's own highlighting — no app-side truncation.
`tokenize='unicode61 remove_diacritics 2'` so non-ASCII content (Czech,
Spanish, Japanese) behaves.

**`scope` is polymorphic, disambiguated by `:`** — chat JIDs contain one
(`web:atlas`, `telegram:user/123`), folder paths don't (`atlas/eng`).
This costs a folder-naming rule: `:` is rejected in folder names. Default
scope is the caller's own folder subtree.

## ACL

Post-fetch filter via `db.JIDRoutedToFolder(chat_jid, caller.folder)` per
result row — the same gate `inspect_messages` uses. `auth.Authorize` is a
yes/no gate, **not a WHERE-clause generator**, so subtree filtering
cannot be claimed at the SQL level. N+1 lookups, but indexed and cheap at
`limit` 20 (max 200). Pushing the filter into SQL via an
`AllowedFolderSubtree(caller)` helper is future work.

## Audit

One row per call (`5/I`): `action="find_messages"`, `result_count`, and
`params_summary` as the standard JSON dump. **The raw query is stored
as-is.** `audit/log.go`'s key-name redaction (`pass|token|secret`)
applies and nothing more — search queries are user input, not secrets. Do
not invent a hash-the-query redaction policy here; it would diverge from
every other audit row.

## Open

- `snippet()`'s 32-token window is a guess; revisit if results clip.
- A bulk `content` rewrite would need
  `INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`. Messages are
  effectively append-only, so this is an operator command to document,
  not a code path.
- Semantic/vector search needs an embedding column plus `sqlite-vss` or
  an external index. Not v1.

FTS5 reference: <https://sqlite.org/fts5.html>
