---
status: spec
depends: [Q-unified-routing, S-jid-format, 5-uniform-mcp-rest, 9-acl-unified]
supersedes: [specs/1/W-slink.md]
---

# specs/5/W — route tokens (unified chat + webhook surface)

## What this solves

The legacy anonymous-token path coupled "token → drop a message into a
group" to one client shape (browser widget) and one URL prefix.
Webhook ingest wants the same primitive. One token table, one handler,
two friendly URL aliases (`/chat/<token>/` and `/hook/<token>`). JID
prefix encodes routing intent at the data layer; the URL prefix is a
label for human readers.

## Usecase

Visitor chat for a tenant. The operator runs `arizuko token issue acme
chat` and gets back `https://krons.fiu.wtf/chat/<token>/`. They paste
the URL on the acme website. Visitors load it, GET serves the widget,
POST appends their message at JID `web:acme`, the SSE channel streams
the agent reply.

GitHub webhook into an engineering subfolder. The agent at `acme/eng`
calls `issue_webhook("github")`, gets back
`https://krons.fiu.wtf/hook/<token>`, and pastes it into the GitHub
repo's webhook settings. Pushes POST to that URL; webd appends the
body as an inbound message at `hook:acme/eng/github`. From there it
flows through normal routing — the agent sees it like any other
inbound and replies (or stays silent).

Multi-source partitioning under one folder. Same `acme/eng` issues
`issue_webhook("linear", jid_suffix="comments")` → JID
`hook:acme/eng/linear/comments`. The `jid_suffix` argument lets one
folder receive several webhook streams without collision. Same writer,
same URL shape, separate JIDs at the agent.

## Worked example

GitHub webhook end-to-end. Agent at `acme/eng` calls:

```json
{ "method": "issue_webhook", "params": { "source_label": "github" } }
```

gated authorizes (`acme/eng` tier 1, mints for self), inserts a row
with `jid="hook:acme/eng/github"`, `owner_folder="acme/eng"`, returns:

```json
{
  "token": "Yp3v...Q2",
  "url": "https://krons.fiu.wtf/hook/Yp3v...Q2",
  "jid": "hook:acme/eng/github"
}
```

Operator pastes the URL into GitHub repo settings. A push fires:

```
POST /hook/Yp3v...Q2
Content-Type: application/json
X-GitHub-Event: push
{"ref":"refs/heads/main","commits":[...]}
```

webd hashes the token, looks up the row, appends an inbound at
`hook:acme/eng/github` with `sender="github"`, body verbatim, and
headers map. Routing per `Q-unified-routing` delivers it to the
`acme/eng` agent, which decides whether to reply.

## The primitive

```sql
CREATE TABLE route_tokens (
  token_hash    BLOB PRIMARY KEY,        -- sha256(token); raw token returned once
  jid           TEXT NOT NULL,           -- web:<folder>[/...] | hook:<folder>/<source>[/...]
  owner_folder  TEXT NOT NULL,           -- issuing folder; bounds revocation (admin authority)
  created_at    TEXT NOT NULL
);
CREATE INDEX route_tokens_jid ON route_tokens(jid);
```

A route token maps a bearer token to a single inbound JID plus an
admin folder. webd hashes the URL token, looks up the row, appends
the body as an inbound message at `row.jid`. Revocation = delete the
row.

`owner_folder` can diverge from the folder embedded in the JID: a
tier-1 agent at `acme` may mint on behalf of descendant `acme/eng`,
in which case `owner_folder="acme"` and the JID targets `acme/eng`.
Revocation follows `owner_folder`, not the JID. Multiple active
tokens per JID are permitted — `token_hash` is the primary key, not
JID.

Token: 32 random bytes, base64url. Stored as `sha256(token)`. Raw
token returned exactly once at issue time.

## URL routing

| Path                | Token JID         | Methods   | Behavior                                                          |
| ------------------- | ----------------- | --------- | ----------------------------------------------------------------- |
| `/chat/<token>/`    | `web:` or `hook:` | GET, POST | GET → widget; POST → message + SSE reply                          |
| `/chat/<token>/mcp` | `web:` or `hook:` | GET, POST | Per-token MCP surface (send_message, get_round, get_round_status) |
| `/hook/<token>`     | `web:` or `hook:` | GET, POST | Alias of `/chat/<token>/` — same handler                          |
| `/chat/`            | (JWT)             | GET, POST | Authenticated chat (no token segment)                             |

Both token URLs route to the same handler. JID prefix determines
sender attribution and default output style, not URL access. Pick
the URL prefix that reads right — `/hook/` for webhook providers,
`/chat/` for browser users. `/chat/` without a token segment routes
to the authenticated handler.

## JID shape (consistent with S-jid-format)

- `web:<folder>[/<suffix>]` — anonymous web chat at folder
- `hook:<folder>/<source>[/<suffix>]` — labeled webhook ingest at folder

The `web:` and `hook:` prefixes are data-layer markers — they drive
sender attribution and default rendering, not URL access.

`<folder>` is the destination folder path (e.g. `acme/eng`).
`<source>` derives from the `source_label` argument at issuance and
also becomes the inbound message's `sender` field. `<suffix>` is the
optional `jid_suffix` argument.

## MCP + REST surface

Per `5/5-uniform-mcp-rest.md`: every action is one hand-written
handler with two faces — MCP for the agent, REST for operators (dashd,
CLI). Sharp tool names per `mcp_tool_naming`:

| Action | MCP                                        | REST                            |
| ------ | ------------------------------------------ | ------------------------------- |
| Issue  | `issue_chat_link(jid_suffix?)`             | `POST /v1/route_tokens/chat`    |
| Issue  | `issue_webhook(source_label, jid_suffix?)` | `POST /v1/route_tokens/hook`    |
| List   | `list_route_tokens()`                      | `GET /v1/route_tokens`          |
| Revoke | `revoke_route_token(jid)`                  | `DELETE /v1/route_tokens/{jid}` |

`issue_chat_link` and `issue_webhook` are distinct tools (distinct
intents, distinct descriptions); they share one internal
`insertRouteToken` writer. `owner_folder` is bound from session
context, never a parameter. Token returned once.

## Where this runs

`gated` owns the schema, MCP handlers, and REST handlers. Per
`9-acl-unified`, gated applies the ACL gate at issue, list, and
revoke.

`webd` reads `route_tokens` at the URL boundary. One handler is
mounted at both `/chat/<token>/` and `/hook/<token>`: GET serves the
widget, POST appends a message (SSE stream for browsers, plain
response otherwise). No ACL gate — the bearer token IS the auth.
webd enforces the per-token rate limit.

Inbound dispatch flows through normal routing per `Q-unified-routing`
— the ACL is re-applied on the JID at message-handling time, same as
any other inbound.

## Issuance sources

Two sources, one writer (`insertRouteToken`):

- Agent via MCP (`issue_chat_link` / `issue_webhook`) — issues for own
  folder or, when permitted by tier, a descendant.
- Operator via dashd / `arizuko token issue` over REST.

No automatic seeding at folder creation. A folder gets a chat token
when someone calls `issue_chat_link` for it — agent on first need,
operator at setup time.

## Authorization

Per `9-acl-unified.md`. Mint scope by tier (lower tier = broader
reach):

| Tier | Mint for           |
| ---- | ------------------ |
| 0    | any folder         |
| 1    | self + descendants |
| 2    | self only          |
| 3+   | no mint            |

Revocation requires `Authorize(principal, admin, owner_folder)` —
agent in folder A cannot revoke a token whose `owner_folder = B`.
Tokens themselves are public bearer credentials — no ACL at the
`/chat/<token>/` or `/hook/<token>` aliases beyond row existence +
rate limit.

## Rate limits, body limits

Per-token rate limit in webd (in-memory bucket). Ceiling chosen by
JID prefix — `hook:` higher than `web:`. Body limit 1 MiB,
env-configurable. Body signature validation (e.g. `X-Hub-Signature`)
is a skill concern, not platform.

## Cutover

No backfill. Only live token (Atlas on marinade) gets reissued by hand.

1. `CREATE TABLE route_tokens` and `DROP COLUMN groups.slink_token`
   in one migration.
2. Delete all `slink*` code paths; replace with `chat`/`hook` handlers.
3. Rename `SLINK_TOKEN` env → `CHAT_TOKEN`.
4. Post-deploy: reissue Atlas's chat token via
   `arizuko token issue marinade/atlas chat`, update the marinade-side embed.

## Tests

- `issue_webhook('github')` from `acme/eng` → POST `/hook/<token>`
  appends at `hook:acme/eng/github`.
- Chat token at `/hook/<token>` succeeds (alias); hook token at
  `/chat/<token>/` succeeds (alias).
- Revoke → next request 401, no grace.
- Agent in folder A cannot revoke folder B's token (ACL scope).
- Tier 1 at `acme` can mint for `acme/eng`; tier 2 at `acme` cannot.
- MCP `issue_chat_link` and REST `POST /v1/route_tokens/chat` produce
  identical rows (one writer, two faces).
