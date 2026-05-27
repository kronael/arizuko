---
status: shipped
shipped: 2026-05-01
extended: 2026-05-27
---

# Message history MCP

Agent-side tools to query message history.

## Tools

### Fetch by location (shipped 2026-05-01)

- `get_history(chat_jid, limit, before)` — paginated chat scroll.
- `get_thread(chat_jid, topic, limit, before)` — narrow to one
  (chat_jid, topic) slice (Telegram forum topics, web-chat topics).
- `fetch_history(chat_jid, limit, before)` — platform-truth fallback.

Source: `ipc/ipc.go` + `webd/mcp.go`. Used by `recall-messages` skill.

### Find by content (extension, draft)

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
    time:     string,                // RFC3339
    role:     "user" | "agent" | "system",
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
`content`, kept in sync by triggers. Bootstrap migration:

```sql
CREATE VIRTUAL TABLE messages_fts USING fts5(
  content,
  content='messages',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

-- Populate from existing rows on first migration.
INSERT INTO messages_fts(rowid, content)
  SELECT id, content FROM messages;

-- Keep in sync.
CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.id, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.id, old.content);
END;
```

`tokenize='unicode61 remove_diacritics 2'` — handles non-ASCII
content (Czech, Spanish, Japanese, ...) without surprises.

**Implementation query:**

```sql
SELECT m.chat_jid, m.sender, m.time, m.role,
       snippet(messages_fts, 0, '«', '»', '…', 32) AS content,
       bm25(messages_fts) AS rank
FROM messages_fts f
JOIN messages m ON m.id = f.rowid
WHERE messages_fts MATCH :query
  AND (:scope IS NULL OR m.chat_jid = :scope OR m.folder = :scope OR m.folder LIKE :scope || '/%')
  AND (:sender IS NULL OR m.sender = :sender)
  AND (:since IS NULL OR m.time >= :since)
ORDER BY rank, m.time DESC
LIMIT :limit;
```

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

Same gate as `get_history`: caller must have `messages:read` on the
matched `scope`. The query above already filters by `m.chat_jid` and
`m.folder` — ACL enforces an extra `WHERE` clause from `acl.Authorize`
to restrict to the caller's allowed folder subtree. Cross-folder
results the caller can't see are excluded at the SQL level (one query,
one WHERE — not fetch-then-filter).

## Audit

One audit row per call (per spec 5/I). Fields:

- `action = "find_messages"`
- `params_summary` = `{query_hash, scope, sender, since, limit}` — the
  raw query string is hashed (sha256, first 8 bytes hex). Audit can
  group calls by hash without storing the search content. This matches
  how other read-tools log per 5/I (params recorded as summary, raw
  args not echoed).
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

- `store/messages.go` — `ListMessages`, `GetMessages` (existing readers).
- `store/migrations/NNNN-messages-fts.sql` — new migration adding the
  virtual table + triggers.
- `ipc/ipc.go` — where `find_messages` registers.
- SQLite FTS5 reference: <https://sqlite.org/fts5.html>
