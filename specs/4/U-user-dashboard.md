---
status: spec
---

# User dashboard (`/me/*`)

A user-facing, authenticated portal: account, orgs, folders, chat
browser + continuation, secrets, costs, invites. Distinct from
`dashd` (`/dash/*`, operator surface). Subsumes today's `/chat/*`
under `webd`.

## Essence

The dashboard is the user's window into everything arizuko knows
_about them_ and _for them_: which identities map to one account,
which orgs/folders they reach with what rights, every conversation
they've ever had across channels, the live ability to continue any
thread from the web, and the per-folder controls (secrets, costs,
sub-folder creation, invites) their grants permit. dashd shows the
_operator_ the system; `/me/*` shows each _user_ their slice — and
nothing else.

## Backend split

Mount `/me/*` in **webd**. Rationale:

- webd already owns user-facing surface (`/slink/*`, `/chat/*`,
  `/mcp`, the SSE hub). Co-locating the dashboard keeps user-facing
  concerns in one daemon.
- dashd is the operator aggregator; merging would conflate audiences
  and complicate auth (operator `**` grant vs. user-scoped reads).
- A new daemon (`userd`/`medash`) duplicates webd's SSE hub, MCP
  bridge, and proxyd header trust path. Rejected on minimality.

`/chat/*` becomes a redirect-or-alias under `/me/chat/*` and is
deleted, not shimmed (per session directive: no backwards-compat).

## Auth

All `/me/*` routes require a valid `proxyd`-signed JWT cookie
(`auth.VerifyHTTP`). Every read scopes by the JWT sub — never by a
URL-supplied sub. Grant enumeration uses `store.MatchingGroups(sub)`
(spec 5/A tactical step) so user-side globs work.

## Route table

| Route                                 | Method      | Purpose                                                            |
| ------------------------------------- | ----------- | ------------------------------------------------------------------ |
| `/me/`                                | GET         | Portal landing (account summary, recent threads, orgs)             |
| `/me/account`                         | GET         | Identity claims, OAuth providers, linked JIDs, sub                 |
| `/me/account/jids`                    | POST/DELETE | Link / unlink JID claim (issues claim invite, redeems)             |
| `/me/account/oauth/{provider}`        | POST/DELETE | Connect / disconnect provider                                      |
| `/me/orgs`                            | GET         | Orgs the user is a member of (worlds: `org/*` glob hits)           |
| `/me/orgs/{org}`                      | GET         | Folder tree under org, filtered by user's grants                   |
| `/me/folders/{folder...}`             | GET         | Folder detail: settings, PERSONA.md, skills, scheduled tasks       |
| `/me/folders/{folder...}/files`       | GET         | Files listing (allow-listed paths only)                            |
| `/me/folders/{folder...}/files/{rel}` | GET/PUT     | Read/write allow-listed memory files (mirrors dashd allow-list)    |
| `/me/folders/{folder...}/subfolders`  | POST        | Create sub-folder (per org policy; requires `admin`)               |
| `/me/folders/{folder...}/invites`     | GET/POST    | List / issue folder-grant invites (admin only)                     |
| `/me/chats`                           | GET         | Conversation index: filter by folder, channel, date, sender        |
| `/me/chats/{folder}/{topic}`          | GET         | Thread view: paginated transcript                                  |
| `/me/chats/{folder}/{topic}/send`     | POST        | Send a message into the thread (via `gated/v1/messages`)           |
| `/me/chats/{folder}/{topic}/sse`      | GET         | SSE stream (subscribes to webd hub keyed `folder/topic`)           |
| `/me/chats/new`                       | GET/POST    | Folder picker + new-thread bootstrap                               |
| `/me/secrets`                         | GET         | List secrets user can see (user-scoped + folder-scoped via grants) |
| `/me/secrets/{scope}/{key}`           | GET/PUT/DEL | Read/write/delete a secret at scope                                |
| `/me/costs`                           | GET         | Per-folder cost-cap status + per-user spend                        |
| `/me/invites`                         | GET         | Outstanding invites (JID-claim, folder-grant, admission queue)     |
| `/me/invites/{id}/accept`             | POST        | Redeem an invite                                                   |
| `/me/settings`                        | GET/PATCH   | Notification prefs, language, default new-chat folder              |
| `/me/x/*`                             | GET         | HTMX partials (lazy tabs, paginators, thread fragments)            |

## UI / rendering

Server-rendered HTMX, mirroring dashd's choice (confirmed:
`dashd/main.go` returns `text/html` with `hx-*` attributes, no SPA).
One renderer per page; partials under `/me/x/*` serve incremental
loads (thread pagination, folder-tree expansion, SSE-driven message
append). No client-side router, no React.

Layout: left rail = orgs/folders tree; main = current view; top bar
= account + open chats. The chat browser is the default landing
when the user has ≥1 thread.

## Chat browser model

Source tables: `messages` (`chat_jid`, `topic`, `routed_to`, `sender`,
`timestamp`, `content`). Conversation key = `(routed_to, topic)`
— `routed_to` is the folder, `topic` the thread within it. `chat_jid`
identifies the channel surface (used for filtering "show me my
Telegram threads").

Index query (sketch): `SELECT routed_to, topic, MAX(timestamp) AS
last, COUNT(*) AS n FROM messages WHERE sender = ? OR routed_to IN
(<user's grant folders>) GROUP BY routed_to, topic ORDER BY last
DESC LIMIT 50`. Pagination by `(last, topic)` cursor.

Thread view: `SELECT * FROM messages WHERE routed_to=? AND topic=?
ORDER BY timestamp DESC LIMIT 100 OFFSET ?`. Render verb, sender,
attachments, content. HTMX paginator at the top ("older").

Visibility rule: a thread is visible if `sender == sub` OR the user
has a grant on `routed_to` (any rights). Lurker threads (channel
member, no personal grant) are visible when the channel route
authorizes them (spec 5/A route-as-auth).

## Chat interface model

Continuing an existing thread: POST `/me/chats/{folder}/{topic}/send`
→ webd writes a `web:` inbound via `gated/v1/messages` with that
`routed_to` + `topic`. The gateway poll loop picks it up; agent
output streams back via the existing SSE hub keyed `folder/topic`.

Starting a new thread: `/me/chats/new` shows a folder picker
populated from the user's grant set (folders where rights ⊇
`interact`). On submit, allocate a new topic id (`web:topic/<rand>`),
post the first message, redirect to `/me/chats/{folder}/{topic}`.

SSE: reuse `hub.go` fan-out. The endpoint subscribes by
`(folder, topic)`; webd's existing `Hub.Publish` from the gated
callback path delivers turns. No new transport.

## Per-org view

Walk `store.MatchingGroups(sub)` → group by first path segment (the
org/world). For each org, render the folder tree (depth-first from
`groups` table where `path GLOB '<org>/*'`), annotate each node with
the user's rights (`interact`, `admin`). Admin-only actions
(invite, sub-folder create) render only on nodes where the user has
`admin`.

## Secrets manager

Scopes: `user:<sub>` (private to the account) and `folder:<path>`
(visible to anyone with `interact` on that folder; writable with
`admin`). Reuse dashd's `me_secrets.go` model — same allow-list,
same write path — but auth-scope to the JWT sub. Audit-log row on
every PUT/DELETE (link to `/me/x/secrets/audit`).

Write-side validation: reject keys outside `[a-zA-Z0-9_./-]`, reject
scope mismatches (cannot write `folder:other/team` without admin
grant). Validate BEFORE persistence.

## Cost view

Source: `cost_log` table (per-call cost in cents, folder, timestamp,
sub) joined with `auth_users.cost_cap_cents_per_day`. Aggregations:

- Per-folder today: `SUM(cost) WHERE folder=? AND date=today`
  vs. folder cap.
- Per-user today: `SUM(cost) WHERE sub=? AND date=today` vs. user
  cap.
- 7-day sparkline per folder (HTMX-loaded partial).

Cap-hit folders rendered red; near-cap (≥80%) amber.

## Open questions

1. **Cross-org folder names.** Two orgs both have `team/eng`. The
   path is globally unique (`acme/team/eng` vs `globex/team/eng`),
   but UI breadcrumbs need org scoping. Confirm the rule.
2. **Anonymous viewing of one's own channel-route threads.** spec
   5/A says anonymous interaction is first-class. Can a sub WITHOUT
   a JID-claim see threads they participated in via a channel route?
   Probably no — the dashboard requires an OAuth-authenticated
   account; channel-only users get no dashboard.
3. **Secret leakage via thread transcript.** If the agent ever
   echoed a secret into a message (it shouldn't), the chat browser
   surfaces it. Do we redact `[A-Z0-9]{20,}` heuristically, or
   trust upstream skill discipline?
4. **JID-claim invite UX.** spec 5/A splits invites into JID-claim
   and folder-grant. Where does the claim flow start — `/me/account`
   issues a token the user pastes into the channel-side bot, or the
   reverse (channel-side `/claim` command issues a URL)?
5. **Pagination scaling.** A power user with 10⁵ messages: cursor
   pagination by `(timestamp, id)` likely fine, but the GROUP BY in
   the index query may need a materialized `conversations` view.
6. **SSE multiplexing.** One browser tab open on three threads = three
   SSE connections. Tolerable, or do we want one connection
   multiplexed by topic? Defer until measured.
7. **Sub-folder creation policy.** Who decides whether a member can
   create sub-folders? Per-org `settings.toml` flag? A new grant
   right (`create`)? Spec 5/A only defines `interact`+`admin`.
8. **Operator (`**`) visibility through `/me/`.** Does an operator
see all threads via `/me/chats`? Likely **no** — `/me/*` is the
   user's view of *themselves*. Operator omniscience belongs to
   `/dash/*`. State this explicitly to prevent drift.
9. **Settings persistence.** New table `user_settings(sub, key,
value)` or extend `auth_users` columns? Schema lives in `gated`
   per CLAUDE.md; webd writes via `/v1/users/{sub}/settings`.
10. **Mobile.** HTMX server-rendered is fine on mobile but the
    folder-tree rail needs collapse behavior. Spec the breakpoint.

## Implementation phases

Each phase is independently shippable. Tests written in the same
commit as the feature (per `ship_with_tests`).

- **M0 — read-only foundation.** `/me/`, `/me/account`, `/me/orgs`,
  `/me/orgs/{org}`, `/me/chats`, `/me/chats/{folder}/{topic}`,
  `/me/settings` (GET only). All reads scope by JWT sub. No writes.
  Subsumes today's `/chat/*` read paths.
- **M1 — chat continuation.** `/me/chats/{folder}/{topic}/send`,
  `/me/chats/{folder}/{topic}/sse`, `/me/chats/new`. Reuses webd's
  SSE hub; `/chat/*` deleted in this phase.
- **M2 — secrets + costs.** `/me/secrets/*`, `/me/costs`,
  `/me/folders/{folder}/files` read paths. Audit-log surface.
- **M3 — admin + invites.** Folder-grant invite issuance,
  sub-folder creation, JID-claim invites, admission-queue view.
  Gated on `admin` rights.

## Dependencies

- spec 5/A — auth consolidation (route-as-auth, user-sub globs).
  `/me/orgs` rendering assumes `MatchingGroups` ships.
- spec 5/32 — org-chart vocabulary (worlds = orgs).
- spec 6/R — `/v1/*` federation; `/me/*` should be a `/v1/*` client
  of gated/onbod where possible (parallel to dashd's migration
  table). Direct DB reads acceptable in M0, migrate in M2/M3.
- `cost_log` (migration 0049), `groups`/`routes` (0051),
  `user_groups` (0013), `messages` (`routed_to`, `topic`).

## Non-goals

- Operator views (stay in `/dash/*`).
- Backwards compatibility for `/chat/*` (deleted in M1).
- Native mobile app or SPA (HTMX only).
- Replacing channel adapters with web-first chat — channels remain
  the primary surface; `/me/chat` is the convenient overflow.
