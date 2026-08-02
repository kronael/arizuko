---
status: shipped
supersedes: [4/U-user-dashboard.md]
---

# webd — the user-facing web surface

`webd` is the browser side of arizuko: a channel adapter that happens
to render HTML. It registers JID prefix `web:` and answers `POST /send`
like `teled`/`discd` do, and it serves the two human surfaces —
the embeddable chat widget and the per-user `/me/*` portal.

Everything here is _user_-facing. Operator views live in `dashd`
(`/dash/*`); the split is by audience, not by feature.

## JID model

Orthogonal fields on `core.Message`:

| Field     | Example          | Meaning      |
| --------- | ---------------- | ------------ |
| `ChatJID` | `web:evangelist` | routing key  |
| `Topic`   | `t1738293847`    | conversation |
| `Sender`  | `google:1234567` | author       |

**Prefix resolution differs by prefix, deliberately.**
`telegram:`/`discord:` resolve via exact DB lookup — a real adapter
registered them. `web:`/`group:` resolve via `groupByFolderLocked`
(folder fallback, no explicit registration), because a web chat is
addressed by the folder it lands in, not by a device the platform
issued. `group:` replaced `local:` as the destination prefix; `local:`
survives because it describes _origin_ (internal / scheduler), not
destination — the two were conflated and splitting them was the fix.

Wire grammar per platform: [`../5/S-jid-format.md`](../5/S-jid-format.md).

## Auth planes

Both resolved at `proxyd` and handed to webd as trusted headers:

- **JWT** → `X-User-Sub` + `X-User-Groups` (`null` = operator,
  `[]` = none, `["folder"]` = specific).
- **Slink / route token** → `X-Folder` + `X-Group-Name` +
  `X-Slink-Token`, plus an IP-keyed anon DoS shield
  (`CHAT_ANON_DOS_RPM`, default 10/min, `proxyd/main.go`). The shield
  is a flood guard, not metering.

URL namespaces: `/slink/*` and `/chat/<token>/*` public (token
resolved at proxyd — see [`../5/W-webhook-routes.md`](../5/W-webhook-routes.md)),
`/api/*` JWT + JSON, `/x/*` JWT + HTMX fragment, `/me/*` JWT + HTML.

## `/me/*` — the per-user portal

The portal is the user's window into what arizuko knows _about them_
and _for them_: the folders they reach, every conversation they've had
across channels, and the ability to continue any thread from the web.
`dashd` shows the operator the system; `/me/*` shows each user their
slice, and nothing else.

**It lives in webd, not dashd.** webd already owns the user-facing
surface (slink, SSE hub, MCP bridge, proxyd header trust); putting the
portal there keeps one audience in one daemon. Merging it into dashd
would conflate audiences and complicate auth (operator `**` grant vs.
user-scoped reads). A third daemon (`userd`/`medash`) was rejected on
minimality — it would duplicate webd's SSE hub, MCP bridge, and header
trust path for nothing. The old `/chat/*` was deleted outright, not
shimmed.

Auth: every `/me/*` route requires a valid proxyd-signed JWT
(`auth.VerifyHTTP`), and every read scopes by the JWT sub — never by a
sub supplied in the URL.

### Shipped routes

| Route                             | Method    | Purpose                                |
| --------------------------------- | --------- | -------------------------------------- |
| `/me/`                            | GET       | Portal landing                         |
| `/me/chats`                       | GET       | Conversation index (filter by folder)  |
| `/me/chats/new`                   | GET/POST  | Folder picker + new-thread bootstrap   |
| `/me/chats/{folder}/{topic}`      | GET       | Thread view, paginated transcript      |
| `/me/chats/{folder}/{topic}/send` | POST      | Send into the thread                   |
| `/me/chats/{folder}/{topic}/sse`  | GET       | SSE stream, keyed `folder/topic`       |
| `/me/folders/{folder...}`         | GET       | Folder detail                          |
| `/me/folders/{folder...}/files`   | GET       | Allow-listed files listing             |
| `/me/settings`                    | GET/PATCH | Per-user preferences                   |
| `/me/x/*`                         | GET       | HTMX partials (folders, chats, thread) |

Handlers in `webd/me.go`. Server-rendered HTMX, one renderer per page,
partials under `/me/x/*` for incremental loads — no SPA, no
client-side router, mirroring dashd's choice.

The **secrets, costs, invites, orgs, and account/JID-claim** pages
designed for `/me/*` did not ship there. The user-facing credential
surface landed on dashd instead as `/dash/me/secrets`, `/dash/me/env`
and `/dash/me/connections` — see
[`../5/14-credentials.md`](../5/14-credentials.md) and
[`../5/15-surrogate-oauth.md`](../5/15-surrogate-oauth.md), which are
canonical for it.

### Chat browser model

Source table is `messages` (`chat_jid`, `topic`, `routed_to`, `sender`,
`timestamp`, `content`). The conversation key is `(routed_to, topic)` —
`routed_to` is the folder, `topic` the thread within it. `chat_jid`
identifies the channel surface, which is what makes "show me my
Telegram threads" a filter rather than a separate query path.

Index: group by `(routed_to, topic)` with `MAX(timestamp)`, cursor
paginated on `(last, topic)`. Thread: filter `routed_to` + `topic`,
newest first, HTMX "older" paginator at the top.

**Visibility rule**: a thread is visible if `sender == sub` OR the user
holds a grant on `routed_to`. Lurker threads — the user is a channel
member with no personal grant — are visible when the channel route
authorizes them, so route-as-auth and grant-as-auth reach the same
answer.

Continuing a thread posts a `web:` inbound with that `routed_to` +
`topic`; routd's poll loop picks it up and agent output streams back
over the existing SSE hub. Starting a thread allocates a new topic id
and posts the first message. No new transport in either case — the
portal is a client of the same pipeline every channel uses.

### Open

- **Secret leakage via transcript.** If an agent ever echoed a secret
  into a message, the chat browser surfaces it. Heuristic redaction
  (`[A-Z0-9]{20,}`) vs. trusting upstream skill discipline — undecided.
- **Pagination at 10⁵ messages.** Cursor pagination is fine; the
  group-by in the index query may need a materialized `conversations`
  view.
- **Sub-folder creation right.** Per-org flag or a new `create` right?
  The ACL model defines `interact` + `admin` only
  ([`../5/32-acl-unified.md`](../5/32-acl-unified.md)).

## Non-goals

- Operator views — they stay in `/dash/*`.
- Backwards compatibility for `/chat/*` (deleted, no shim).
- Native mobile app or SPA. HTMX only.
- Replacing channel adapters with web-first chat. Channels remain the
  primary surface; the web portal is the convenient overflow.
- Agent event stream in the widget (thinking, tool calls, streaming).
