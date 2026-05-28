---
status: shipped
shipped: 2026-05-01
extended: 2026-05-27
---

# Message history MCP

Agent-side tools to query message history.

## Tools

### Fetch by location (shipped 2026-05-01)

- `inspect_messages(chat_jid, limit, before)` — paginated chat scroll.
  Replaces deprecated `get_history` alias.
- `get_thread(chat_jid, topic, limit, before)` — narrow to one
  (chat_jid, topic) slice (Telegram forum topics, web-chat topics).
- `fetch_history(chat_jid, limit, before)` — platform-truth fallback.

Source: `ipc/ipc.go` + `webd/mcp.go`. Used by `recall-messages` skill.

### Find by content (extension, shipped 2026-05-27)

One tool. **SQLite FTS5** under the hood — tokenized, ranked,
phrase + boolean operators.

```
find_messages(
  query:    string,                  // required; FTS5 query syntax
  scope:    string  | null,          // optional; chat_jid or folder. Default: caller's own folder subtree.
  sender:   string  | null,          // optional; exact match on sender column
  since:    string  | null,          // optional; RFC3339 timestamp lower bound
  limit:    int     | null,          // optional; default 20, max 200
) → [
  {
    chat_jid: string,
    sender:   string,
    timestamp: string,               // RFC3339 — matches the `messages.timestamp` column
    is_from_me: bool,                // true = user, false = inbound from platform
    is_bot_message: bool,            // bot/system origin
    content:  string,                // snippet around the match, ~500 chars
    rank:     number                 // BM25 score (lower = better match)
  }, ...
]
```

**Query syntax — full FTS5:**

| Pattern                  | Meaning                                          |
| ------------------------ | ------------------------------------------------ |
| `budget`                 | match the word `budget` (tokenized, case-folded) |
| `"budget meeting"`       | exact phrase                                     |
| `budget OR plan`         | either word                                      |
| `budget AND NOT meeting` | budget without meeting                           |
| `budg*`                  | prefix match (suffix wildcard only)              |
| `NEAR(budget plan, 5)`   | both words within 5 tokens                       |

Standard FTS5 syntax — operators see the same query language SQLite
documents. No second DSL invented.

**Storage:** a virtual table `messages_fts` shadows `messages` on
`content`, kept in sync by triggers. **Important:** `messages.id` is
`TEXT PRIMARY KEY` (UUID-shaped); FTS5 requires an INTEGER rowid for
external-content shadowing. Use SQLite's implicit `rowid` (every
non-WITHOUT-ROWID table has one alongside the TEXT PK) — DO NOT
attempt to use `id` as the FTS rowid.

```sql
CREATE VIRTUAL TABLE messages_fts USING fts5(
  content,
  content='messages',
  -- content_rowid defaults to 'rowid' (the implicit INTEGER) — explicit:
  content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);

-- Populate from existing rows on first migration.
INSERT INTO messages_fts(rowid, content)
  SELECT rowid, content FROM messages;

-- Keep in sync.
CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.rowid, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.rowid, old.content);
END;
```

`tokenize='unicode61 remove_diacritics 2'` — handles non-ASCII
content (Czech, Spanish, Japanese, ...) without surprises.

**Implementation query** (real column names verified against
`store/messages.go`):

```sql
SELECT m.chat_jid, m.sender, m.timestamp, m.is_from_me, m.is_bot_message,
       snippet(messages_fts, 0, '«', '»', '…', 32) AS content,
       bm25(messages_fts) AS rank
FROM messages_fts f
JOIN messages m ON m.rowid = f.rowid
WHERE messages_fts MATCH :query
  AND (:scope IS NULL OR m.chat_jid = :scope OR m.routed_to = :scope OR m.routed_to LIKE :scope || '/%')
  AND (:sender IS NULL OR m.sender = :sender)
  AND (:since IS NULL OR m.timestamp >= :since)
ORDER BY rank, m.timestamp DESC
LIMIT :limit;
```

Columns: `messages.timestamp` (not `time`), `messages.routed_to` —
the per-message folder attribution (added in migration 0015; the older
`group_folder` was dropped in 0023 as write-only dead weight). JOIN on
`m.rowid = f.rowid` (since `m.id` is TEXT, not the FTS rowid).
`snippet()` returns the matched fragment with FTS5's built-in
highlighting (`«match»…surrounding…`). No app-side truncation logic.

**`scope` polymorphism:** chat_jid contains `:` (e.g. `web:atlas`,
`telegram:user/123`); folder paths don't (e.g. `atlas/eng`). Disambiguate
on parse:

```go
if strings.Contains(scope, ":") {
    // chat_jid filter
} else {
    // folder subtree filter
}
```

Edge: if an operator ever names a folder `foo:bar`, the parse breaks.
v1 ships with a folder validation rule rejecting `:` in folder names
(none today).

**SQL injection:** `:query` is bound as a parameter. FTS5 parses it as
its own syntax — no SQL escape needed for the inner query. Malformed
FTS5 syntax returns a structured error (`SQLITE_ERROR`), surfaced to
the caller as `400 invalid query`.

## ACL

Same gate as `inspect_messages`: post-fetch filter via
`db.JIDRoutedToFolder(chat_jid, caller.folder)` per result row, with
tier-0 (operator) bypassing. `auth.Authorize` today is a yes/no gate,
not a WHERE-clause generator — don't claim subtree filtering at the SQL
level. Pattern matches the `inspect_messages` handler in `ipc/ipc.go`.
N+1 calls per result, but indexed and cheap; for default `limit=20` and
max `200`, total overhead is sub-millisecond.

Future work: introduce `acl.AllowedFolderSubtree(caller) []string`
helper and push the filter into the SQL `WHERE` clause. Not v1.

## Audit

One audit row per call (per spec 5/I). Fields:

- `action = "find_messages"`
- `params_summary` = the standard JSON dump per `audit/log.go` — raw
  query stored as-is, key-name redaction applies only to keys matching
  `pass|token|secret` (no special redaction for the search query).
  Search queries are user input, not secrets; the existing audit
  policy handles them correctly. **Do not invent a sha256 hash
  redaction policy here** — would diverge from every other audit row.
- `result_count` = number of rows returned

## Open questions

1. **Body length cap on `snippet()`.** FTS5's `snippet()` takes a
   token-count window (currently 32 tokens). Reasonable default; revisit
   if results clip too aggressively in practice.
2. **Rebuilding the FTS index.** If `content` is updated in bulk (very
   rare — messages are append-only mostly), an `INSERT INTO
messages_fts(messages_fts) VALUES('rebuild')` may be needed.
   Document the operator command; not v1 path.
3. **Future: semantic / vector search.** FTS5 covers keyword match.
   Semantic similarity needs an embedding column + vector search
   (`sqlite-vss` extension or external). Not in v1.

## Pointers

- `store/messages.go` — `FindMessages` (FTS reader), `ListMessages`,
  `GetMessages` (existing readers).
- `store/migrations/0070-messages-fts.sql` — migration adding the
  virtual table + triggers.
- `ipc/ipc.go` — where `find_messages` registers.
- SQLite FTS5 reference: <https://sqlite.org/fts5.html>
