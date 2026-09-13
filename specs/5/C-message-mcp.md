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

## Decision: one small grammar, and the tool says what it is

`find_messages` runs routd's in-memory BM25 index over the `messages`
entity (`routd/msgsearch`, spec `6/42` §8). The grammar is **terms,
`"phrases"` and `-exclusions`**: every term must appear, a phrase matches
its words adjacent and in order, and a `-` in front of either excludes
it. There is no `OR`, no `prefix*` and no `NEAR`, and the MCP tool's
description states exactly that list — a description advertising an
operator the engine does not run hands the agent a silent empty result,
which it reports as "no such message". A malformed query (an unclosed
phrase, nothing to match) reaches the caller as an error, never as an
empty hit list.

**The index is derived and the store is the record.** A document is
addressed by the message's own coordinate, the chat and the position, so
a hit can only be reported under the message it was made from. routd
walks every chat into the index at boot and feeds it from the four writes
that touch an indexed attribute — the append, the media rewrite, the
observe stamp, the breaker's prune — and until that walk finishes a
search is REFUSED rather than answered empty. A term is a run of Unicode
letters and digits, lower-cased, with diacritics dropped by NFD
decomposition, so "uroven" answers "úroveň". The snippet is a 32-token
window around the first hit with «» around each match. The ranking is
BM25 with SQLite FTS5's own constants and negated sign, and the top ten
is FTS5's top ten for an exact-term query
(`routd/msgsearch/parity_test.go`).

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

- The snippet's 32-token window is a guess; revisit if results clip.
- A write another process makes — `arizuko send`, an archive import, a
  group delete — is invisible to the index until the next boot walk.
  Messages are effectively append-only, so the answer is a restart, not a
  rebuild command.
- Semantic/vector search needs an embedding and an index to hold it. Not
  v1.

The ranking's reference, for the constants and the sign:
<https://sqlite.org/fts5.html#the_bm25_function>
