---
status: draft
depends:
  [
    1-adoption-interop,
    16-daemon-standalone-matrix,
    ../5/17-openapi-mcp,
    ../5/W-webhook-routes,
  ]
---

# specs/6/18 — connecting Hermes to arizuko's messaging plane

## What this solves

arizuko is not an assistant. It is the plane an assistant plugs into:
ingress, persistence, routing, audit, grants, secrets, egress. Hermes
(`NousResearch/hermes-agent`) is a harness — one agent, many turns — and
it currently carries its own connector layer to reach users. Those two
facts compose: Hermes should stop owning transport, and arizuko should
stop pretending it needs to own the agent.

Per [`6/1`](1-adoption-interop.md): adoption is addition, not
replacement. This spec is the concrete instance of that thesis.

## The interface already matches

Hermes's `BasePlatformAdapter` (`gateway/platforms/base.py`) requires
`connect`, `disconnect`, `send`, `send_typing`, `send_image`,
`get_chat_info`, and optionally `edit_message`, `send_document`,
`send_voice`, `send_video`, `send_animation`, `send_image_file`.
Inbound dispatch is a callback registered via `set_message_handler`,
delivering a `MessageEvent`.

arizuko's `chanlib.BotHandler` requires `Send`, `Typing`, `SendFile`,
`SendVoice`, `Edit`, `Delete`, `Post`, `Like`, `Dislike`, `Forward`,
`Quote`, `Repost`, `Pin`, `Unpin`, `FetchHistory`. Inbound dispatch is a
POST of `chanlib.InboundMsg` to routd.

These are the same interface with different spelling. That is not a
coincidence — both are the minimal verb set a chat platform exposes —
and it means the adaptation is a mapping, not an architecture.

**Measured**: Hermes's gateway is 39,005 LOC, of which
`gateway/platforms/` is 26,102 across 18 platforms. arizuko's adapters
are ~25k own LOC across ~10 platforms plus a vendored discordgo fork.

## Two directions, both valid

The interface match runs both ways, so there are two integrations, and
they are not alternatives.

### Mode A — Hermes runs inside arizuko

runed spawns "just another binary": `cfg.Image` selects the container
image (`container/runner.go:700`), and the agent reaches the platform
over MCP through a socat bridge (`STDIO ↔ UNIX-CONNECT:/run/ipc/gated.sock`,
`container/runner.go:860`). Hermes is already an MCP client with stdio
transport (`tools/mcp_tool.py`, `mcp_servers` in `cli-config.yaml`), so
the seam needs configuration, not a port.

Hermes then deletes `gateway/platforms/` for any platform arizuko
carries, and gains audit, persistence, grants, secrets, tenancy, and
egress isolation it does not have today.

### Mode B — Hermes's gateway becomes an arizuko edge

Hermes's 18 platform adapters already implement arizuko's verb set.
Wrapped as channel edges they give arizuko Feishu, Matrix, WeCom, Weixin,
DingTalk, SMS, Mattermost, BlueBubbles, and Home Assistant — nine
platforms it does not have.

**Mode B first.** It is cheaper, reversible, and requires no change to
how either project runs its agent. Mode A is the deeper win and should
follow once B has proven the envelope mapping.

## What Hermes gains

Each item is a property arizuko enforces structurally, not a feature
Hermes could add cheaply:

1. **Persistence** — every message a row in `routd.db`, with FTS5 search
   (`find_messages`, `specs/5/C`). Hermes has session state, not a
   queryable message corpus.
2. **Audit** — one `audit_log` row per state transition, emitted in the
   same transaction as the mutation via `audit.EmitInTx`
   (`specs/5/I`). Not a log line that can be dropped.
3. **Grants** — `auth.Authorize` is the sole runtime evaluator; every
   tool call is gated by `(action, scope, params)` bound to the caller's
   folder (`specs/4/9`). Hermes has no per-user authorization over its
   own tools.
4. **Secrets injection** — folder- and user-scoped secrets resolved by
   routd and merged into the spawn env (`container/runner.go:266`), with
   connector secrets broker-resolved at tool-call time (`specs/7/Y`) so
   they never sit in the process environment.
5. **Tenancy** — the folder coordinate is tenant, ACL, route, egress,
   web host, and file tree at once. One Hermes deployment serves many
   tenants without Hermes knowing tenancy exists.
6. **Egress isolation** — per-folder network rules through crackbox.

## Token distribution

Six credential kinds, each with one job. This is the part an integrator
must get right; nothing else in the system is as easy to get subtly wrong.

| Credential                       | Minted by                             | Held by               | Authorizes                                                                                                          |
| -------------------------------- | ------------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `AUTH_SECRET` / `CHANNEL_SECRET` | operator `.env`                       | authd, adapters       | bootstrap — the right to ask for a real token                                                                       |
| `service:<daemon>` ES256 JWT     | authd (JWKS-verified)                 | each daemon           | that daemon's own scopes, e.g. `messages:write`                                                                     |
| `service:proxyd` ES256 JWT       | authd                                 | proxyd only           | trust-stamping `X-User-*` inward; the pin is exactly `service:proxyd` and is load-bearing (`auth/middleware.go:14`) |
| route token                      | routd, opaque 256-bit, sha256 at rest | whoever holds the URL | append at **one** JID; `/chat/<t>/`, `/hook/<t>`, `/chat/<t>/mcp`                                                   |
| MCP socket                       | runed, per folder                     | the spawned agent     | folder identity by socket path + `SO_PEERCRED`; tools gated by `db.Authorize(sub, folder, "mcp:"+tool, params)`     |
| user JWT                         | authd via OAuth                       | browser session       | operator/user scopes, folder-contained                                                                              |

Rules an integrator must not break:

- A daemon exchanges the bootstrap secret under its **daemon principal**
  (`AUTHD_SERVICE_NAME`, e.g. `teled`), never under a channel name — the
  mismatch 401s silently and outbound dies.
- A route token is append-at-one-JID. It is not a capability to be
  widened; a URL pasted on a website must never carry verb authority.
- Trust-stamped identity headers are trusted **only** behind the
  `service:proxyd` pin. Widening that pin to `service:*` would let any
  adapter assert an operator subject.

## Known gaps this integration inherits

- **No platform principal.** `chanlib.InboundMsg` carries `Sender` as a
  display string; no `auth.Caller` is ever built from it. A Hermes user
  arriving over Telegram cannot hold a grant. Fixing this is the edge-
  attestation work — arizuko must bind `service:teled → telegram:*` and
  its allowed JID prefixes **at routd**, never trusting edge-supplied
  groups.
- **Containment is per-handler, not per-store.** `BUGS.md` T1: the
  chat-token MCP `get_round` reads any turn in the instance because
  `store.TurnFrames` has no JID predicate while its HTTP twin checks one.
  Adding a second consumer of that surface multiplies the class.

Both must close before Mode A ships to a tenant that isn't the operator.
