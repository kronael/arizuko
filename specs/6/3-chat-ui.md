---
status: shipped
---

# Web Chat UI

A browser-based chat interface for arizuko. Users pick a group (agent),
start or resume topic-based conversations, and see live agent responses
via SSE. Implemented as a channel adapter (`webd`) — same contract as
`teled`, `discd`, etc.

---

## Problem

No web interface existed. Agents were only reachable via Telegram,
Discord, etc. The web channel fills the gap without changing gated or
the routing model.

---

## Design

**webd as channel adapter.** webd registers with gated using JID prefix
`web:`, handles gated callbacks at `POST /send`, and writes bot responses
to the store + SSE hub. gated sees no difference between web and any
other channel.

**proxyd as auth oracle.** proxyd validates all credentials before
forwarding to webd. webd reads injected headers as fact and uses them
only to scope store queries — never validates tokens itself.

**JID model.** Three orthogonal fields:

| Field     | Example          | Meaning                   |
| --------- | ---------------- | ------------------------- |
| `ChatJID` | `web:evangelist` | which agent (routing key) |
| `Topic`   | `t1738293847`    | which conversation thread |
| `Sender`  | `google:1234567` | who wrote it              |

Topic is a field on `core.Message`, not in the JID. Multiple users and
topics coexist under one JID; the SSE hub delivers to the right browser
by `(folder, topic)`.

**JID prefix conventions:**

| Prefix      | Resolves via    | Reply channel                             |
| ----------- | --------------- | ----------------------------------------- |
| `telegram:` | exact DB lookup | teled                                     |
| `discord:`  | exact DB lookup | discd                                     |
| `web:`      | folder fallback | webd                                      |
| `group:`    | folder fallback | none (fire-and-forget, replaces `local:`) |

`web:` and `group:` resolve via `groupByFolderLocked` — no explicit DB
registration needed. `group:` replaces `local:` everywhere.

**Auth planes.** Two separate credential mechanisms, both resolved at proxyd:

- JWT plane: `X-User-Sub` + `X-User-Groups` injected after JWT validation.
  `groups: null` = operator (no restriction), `groups: []` = no access,
  `groups: ["folder"]` = specific folders. Populated from `user_groups`
  table at login.
- Slink plane: `X-Folder` + `X-Group-Name` + `X-Slink-Token` injected
  after proxyd resolves the slink token against the store. Rate-limited
  at proxyd (10 req/min per IP).

**Three URL namespaces:**

| Prefix     | Auth        | Format        |
| ---------- | ----------- | ------------- |
| `/slink/*` | slink token | HTML fragment |
| `/api/*`   | JWT         | JSON          |
| `/x/*`     | JWT         | HTML fragment |

---

## Code

`webd/` — channel adapter daemon:

- `main.go` — config, startup, channel registration with gated
- `server.go` — HTTP mux, `requireUser` middleware (trusts `X-User-Sub`)
- `channel.go` — `POST /send`: writes bot message to store + publishes SSE
- `hub.go` — SSE hub keyed by `folder/topic`
- `slink.go` — `POST /slink/<token>` (inbound), `GET /slink/stream` (SSE)
- `pages.go` — `GET /` (group grid), `GET /chat/<folder>` (chat view)
- `api.go` — `/api/*` JSON endpoints
- `partials.go` — `/x/*` HTMX fragments

`store/messages.go` — `Topics`, `MessagesByTopic`, `MessagesSinceTopic`,
`GroupBySlinkToken`, `GroupByFolder`, `TopicByMessageID`,
`MessageTimestampByID` added for webd's data queries.

`proxyd/main.go` — `WEBD_ADDR` config, routes `/slink/*` and `/*` to
webd, injects `X-User-Sub` + `X-User-Name` after JWT validation.

`core/types.go` — `Topic string` added to `core.Message`.

---

## Gaps (shipped)

All gaps implemented. `local:` was NOT renamed to `group:` — `local:` describes
the origin (internal/scheduler), not the destination, so the name is correct.

---

## Out of scope

Agent event stream (thinking, tool calls, streaming) — next spec:
`specs/web-events.md`.
