---
status: shipped
---

# Web chat UI

Browser chat interface implemented as a channel adapter (`webd/`) —
same contract as `teled`/`discd`. Registers JID prefix `web:`, handles
gated callbacks at `POST /send`, writes bot responses to store + SSE
hub.

JID model (orthogonal fields on `core.Message`):

| Field     | Example          | Meaning      |
| --------- | ---------------- | ------------ |
| `ChatJID` | `web:evangelist` | routing key  |
| `Topic`   | `t1738293847`    | conversation |
| `Sender`  | `google:1234567` | author       |

JID prefix resolution: `telegram:`/`discord:` via exact DB lookup;
`web:`/`group:` via `groupByFolderLocked` (folder fallback, no
explicit registration). `group:` replaces `local:`.

Auth planes (resolved at proxyd):

- JWT → `X-User-Sub` + `X-User-Groups` (`null` = operator,
  `[]` = none, `["folder"]` = specific).
- Slink → `X-Folder` + `X-Group-Name` + `X-Slink-Token` with
  10 req/min/IP rate limit.

URL namespaces: `/slink/*` (slink, HTML fragment), `/api/*` (JWT, JSON),
`/x/*` (JWT, HTMX fragment).

Note: `local:` prefix retained — it describes origin (internal/
scheduler), not destination.

Out of scope: agent event stream (thinking, tool calls, streaming).
