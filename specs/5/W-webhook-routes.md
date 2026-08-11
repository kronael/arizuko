---
status: shipped
shipped: 2026-05-18
depends: [Q-unified-routing, S-jid-format, 17-openapi-mcp, 32-acl-unified]
supersedes: [specs/1/W-slink.md]
---

# specs/5/W — route tokens (unified chat + webhook surface)

## What this solves

The legacy anonymous-token path coupled "token → drop a message into a
group" to one client shape (browser widget) and one URL prefix. Webhook
ingest wants the same primitive at a different surface. **One token
table, one writer, two URL prefixes** — `/chat/<token>/` for humans,
`/hook/<token>` for machines.

## Three orthogonal axes

A route-token URL composes three independent things. None entangles with
another; each has its own storage, lifecycle, and revocation.

1. **Routing (the token).** WHICH chat/folder the URL feeds. Opaque
   256-bit random secret, `sha256` at rest, resolved by DB lookup. Not a
   JWT — nothing is encoded in it; revoke = delete the row.
2. **Identity (optional JWT overlay).** WHO is posting. The same URL
   serves logged-in and anonymous callers: `webd/route_token.go`
   `handleChatTokenPost` stamps the real `sub`/name from the
   proxyd-verified identity headers when the caller is identified, else
   `anon:<ip-hash>` / "Anonymous". The link doesn't change; the session
   does.
3. **Context (optional per-link instructions).** HOW to process what
   arrives through this link. See § Link context.

## The primitive

`route_tokens` (`routd/migrations/0018-route-token-context.sql` for the
`context` column; original table in `store/migrations/0059`). A token
maps a bearer secret to one inbound JID plus an admin folder. 32 random
bytes, base64url, stored as `sha256(token)`, raw value returned exactly
once at issue.

- **`owner_folder` bounds revocation, and may diverge from the JID's
  folder** — an agent at `acme` may mint on behalf of `acme/eng`, giving
  `owner_folder="acme"` with a JID targeting `acme/eng`. Revocation
  follows `owner_folder`, not the JID.
- **Multiple active tokens per JID are permitted.** The PK is
  `token_hash`, not `jid` — a second mint is a distinct token, never an
  error. Revocation by JID deletes all of that JID's tokens under the
  caller's `owner_folder`.
- **Validation (pinned):** `target_folder` must equal or descend from
  `owner_folder`; `source_label` and `jid_suffix` must match `[\w-]+`
  per segment — they become JID path segments, so `/`, whitespace and
  `:` are rejected.

## JID shape (consistent with S-jid-format)

- `web:<folder>[/<suffix>]` — anonymous web chat at folder
- `hook:<folder>/<source>[/<suffix>]` — labeled webhook ingest at folder

`<source>` comes from the `source_label` argument at issuance and also
becomes the inbound message's `sender`. `jid_suffix` lets one folder
receive several streams without collision
(`hook:acme/eng/linear/comments`) — same writer, same URL shape,
separate JIDs at the agent.

## URL routing

Handlers `webd/server.go:101-112`.

| Path                | Methods   | Behavior                                             |
| ------------------- | --------- | ---------------------------------------------------- |
| `/chat/<token>/`    | GET, POST | GET → widget; POST → append + SSE reply              |
| `/chat/<token>/mcp` | POST      | 3-tool MCP surface (§ MCP transport)                 |
| `/chat/stream`      | GET       | SSE stream for a token's `(folder, topic)`           |
| `/hook/<token>`     | POST      | append body as one inbound, 204, no response channel |
| `/panel/<folder>`   | GET, POST | authenticated operator chat — no token segment       |

**Both prefixes accept any valid token regardless of its JID kind.** The
kind is metadata for the agent, not a URL gate: the issuance verb picks
the canonical URL that gets printed to the operator
(`issue_chat_link` → `/chat/`, `issue_webhook` → `/hook/`), but
re-pasting a token at the other prefix gets that surface's contract.
Token not found → 404 at either. The authenticated operator chat lives
at `/panel/` rather than `/chat/` so the public prefix stays
unambiguous. `/slink/<token>/…` is a 301 to `/chat/<token>/…`
(`webd/server.go:117-120`) — the pre-5/W URL, kept as a redirect only.

## The group is the auth boundary

Possessing the token IS group membership — no JWT, no bearer, no ACL at
`/chat/<token>/` or `/hook/<token>` beyond row existence plus webd's
per-token rate limit (in-memory bucket, ceiling chosen by JID prefix,
`hook:` higher than `web:`; body limit 1 MiB, env-configurable).

Per-sender scoping _within_ a group was rejected: it fights the
shared-context model. "Can you access this group" is the only auth
question the public surface answers. Body-signature validation
(`X-Hub-Signature` and friends) is a skill concern, not platform.

Inbound then flows through normal routing — the ACL is re-applied on the
JID at message-handling time, same as any other inbound.

## MCP transport (`/chat/<token>/mcp`)

`webd/chat_mcp.go`. Three tools scoped to the token's group:
`send_message(content, topic?)` → `{turn_id}` (the round handle),
`get_round(turn_id, after?, wait?)`, `get_round_status(turn_id)`.
External agents register the URL as a remote MCP server in their
`mcpServers` config and talk to the group as if the tools were local.

Streaming semantics, the hub, and why blocking-poll is a first-class
dual of SSE rather than a fallback: [`J-sse.md`](J-sse.md).

## Link context (axis 3)

A token may carry issuer-authored instructions for the data arriving
through it — "these are bug reports from the acme website, triage and
file, don't chat back". The instructions belong to the LINK, not to the
folder (one folder can serve several links with different contracts) and
not to the sender (axis 2 is who; axis 3 is how). Optional `context`
argument at mint; omitted → NULL → identical to a pre-context token.
Immutable per token — to change the contract, mint a new link and revoke
the old, the same lifecycle as the secret itself.

**Carry: snapshot at ingest, not lookup at prompt-build.** webd already
holds the token row when a message arrives, copies `row.Context` onto
the inbound (`chanlib.InboundMsg.LinkContext`), routd persists it as
`messages.link_context`, and prompt-build reads it off the trigger row
(`routd/prompt.go` `linkContextBlock`, newest-wins within a batch).
Chosen over a `chat_jid`→token lookup at build time because:

- a JID may have several active tokens, so JID→context is ambiguous;
- identity (axis 2) is already snapshotted onto the row at ingest;
  context rides the same pattern — the row is the complete record of
  what arrived, under which contract;
- revoking or re-minting never retroactively reinterprets messages that
  already arrived.

Rendered as one sibling `<link-context>` tag in the turn envelope, only
when the newest trigger message carries a non-empty value. The agent
treats it as the issuer's handling instructions, not a user request
(`ant/skills/self/chat-link.md`). Messages that did not arrive through a
route token never produce the tag.

## MCP + REST surface

Per [`17-openapi-mcp.md`](17-openapi-mcp.md), one registration with two
faces. The resource is `resreg/resources/route_tokens.go`; handlers +
ACL gate in `routd/route_tokens_resource.go` and `routd/tokens_http.go`.

| Action | MCP                                                  | REST                            |
| ------ | ---------------------------------------------------- | ------------------------------- |
| Issue  | `issue_chat_link(jid_suffix?, context?)`             | `POST /v1/route_tokens/chat`    |
| Issue  | `issue_webhook(source_label, jid_suffix?, context?)` | `POST /v1/route_tokens/hook`    |
| List   | `list_tokens()`                                      | `GET /v1/route_tokens`          |
| Revoke | `revoke_token(jid)`                                  | `DELETE /v1/route_tokens/{jid}` |

`issue_chat_link` and `issue_webhook` are distinct tools (distinct
intents, distinct descriptions) sharing one internal writer.
`owner_folder` is bound from session context on the MCP face, never a
parameter. List never returns a raw token.

Those four are the MANAGEMENT face — mint, list, revoke. Token DELIVERY
is a separate concern with no REST face at all; see § Resolution.

## Resolution (who reads the table)

routd owns and migrates `route_tokens` in `routd.db`. Two daemons READ it
directly, in-process, on the request path:

- `proxyd` `dispatchRouteToken` — resolves the URL token to stamp
  `X-Folder` / `X-Group-Name` / `X-Chat-Token` before forwarding.
- `webd` `lookupRouteToken` — resolves it for the row the handler needs
  (JID + `context`).

**There is no HTTP resolve hop, by design.** Both are FS-mounted on
`store/` (compose `webdService` / `proxydService` `dataSubdirs`), and
split write-discipline lets an FS-mounted daemon read the owner's DB
directly; only NON-mounted daemons must go through the owner's HTTP API.

This spec previously specified the opposite — a `POST
/v1/route_tokens/resolve` call, with webd never opening `routd.db`. That
endpoint was built and shipped, and had zero production callers for its
whole life. It is deleted (BUGS `F13`). Recorded so it does not come back:

- **It bought no containment.** `handleTokenResolve` took a raw token and
  no folder, and applied no folder check — the same shape as the direct
  read. It added only a `routes:read` scope check on webd's own service
  token, which authenticates the daemon, not the tenant. Resolution is a
  reverse lookup on a secret the caller already holds; there is no
  caller-supplied folder to contain it to.
- **The folder is never caller-supplied.** Both readers derive it from
  the resolved row (`groupfolder.JidFolder(row.JID)`). The one surface
  where a caller names a folder beside a token — `/chat/stream?group=` —
  binds it to proxyd's token-derived `X-Folder` stamp
  (`handleRouteTokenStream`), tested adversarially by
  `TestRouteTokenStream_FolderMismatchForbidden`.
- **It added a failure mode.** A per-request HTTP hop on the hot path
  fails every `/chat/` and `/hook/` request whenever routd is briefly
  unreachable, for no gain. Pinned by `TestRouteToken_ResolvedInProcess`.

`RouteTokensEndpoints` is therefore exhaustive for `/v1/route_tokens`:
routd serves no path there the resource does not declare, so the mux and
`/openapi.json` cannot disagree
(`TestRouteTokens_NoHandRolledResolve`).

## Authorization

One evaluator over ACL rows, deny-wins
([`32-acl-unified.md`](32-acl-unified.md), `auth/authorize.go:25`) — the
depth-derived tier table this spec originally carried is gone. Mint and
revoke are authorized against `owner_folder`: an agent in folder A
cannot revoke a token whose `owner_folder` is B. The tokens themselves
stay public bearer credentials.

## Issuance sources

Two sources, one writer: the agent via MCP (own folder, or a descendant
where its grants reach) and the operator via dashd / `arizuko token
issue` over REST. **No automatic seeding at folder creation** — a folder
gets a chat token when someone asks for one.
