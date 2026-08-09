---
status: reference
depends:
  [
    ../6/1-adoption-interop,
    ../6/16-daemon-standalone-matrix,
    17-openapi-mcp,
    W-webhook-routes,
  ]
---

# specs/5/35 — connecting Hermes to arizuko's messaging plane

> **Terminal state: `reference` (2026-08-07).** An interop analysis, not a
> committed build: it establishes that Hermes's adapter interface and arizuko's
> `chanlib.BotHandler` are the same verb set differently spelled, that the
> adaptation is a mapping rather than an architecture, and what must hold before
> Mode A reaches a non-operator tenant. Starting it is a `6/1` adoption
> decision, not a `specs/5` task — and the two inherited gaps it names
> (scheme-bound-but-not-namespace-bound senders; per-handler containment) are
> tracked where they live, not here.

## What this solves

arizuko is not an assistant. It is the plane an assistant plugs into:
ingress, persistence, routing, audit, grants, secrets, egress. Hermes
(`NousResearch/hermes-agent`) is a harness — one agent, many turns — and
it carries its own connector layer to reach users. Those two facts
compose: Hermes should stop owning transport, and arizuko should stop
pretending it needs to own the agent. Per
[`6/1`](../6/1-adoption-interop.md), adoption is addition, not
replacement; this spec is the concrete instance.

## The interface already matches

Hermes's `BasePlatformAdapter` (`gateway/platforms/base.py`) requires
`connect`/`disconnect`/`send`/`send_typing`/`send_image`/`get_chat_info`
plus optional `edit_message`, `send_document`, `send_voice`, …; inbound
dispatch is a `set_message_handler` callback delivering a `MessageEvent`.
arizuko's `chanlib.BotHandler` requires `Send`, `Typing`, `SendFile`,
`SendVoice`, `Edit`, `Delete`, `Post`, `Like`, `Dislike`, `Forward`,
`Quote`, `Repost`, `Pin`, `Unpin`, `FetchHistory`; inbound dispatch is a
POST of `chanlib.InboundMsg` to routd.

Same interface, different spelling — not a coincidence, it is the minimal
verb set a chat platform exposes. **The adaptation is a mapping, not an
architecture.** Measured: Hermes's gateway is 39,005 LOC of which
`gateway/platforms/` is 26,102 across 18 platforms; arizuko's adapters are
~25k own LOC across ~10 platforms.

## Two directions, both valid — **Mode B first**

- **Mode A — Hermes runs inside arizuko.** runed spawns "just another
  binary" (`cfg.Image` selects the image, `container/runner.go`), and the
  agent reaches the platform over MCP through the socat bridge
  (`container/runner.go`). Hermes is already an MCP client with stdio
  transport, so the seam needs configuration, not a port. Hermes then
  deletes `gateway/platforms/` for anything arizuko carries.
- **Mode B — Hermes's gateway becomes an arizuko edge.** Its 18 adapters
  already implement arizuko's verb set; wrapped as channel edges they add
  Feishu, Matrix, WeCom, Weixin, DingTalk, SMS, Mattermost, BlueBubbles,
  and Home Assistant — nine platforms arizuko lacks.

**Mode B first** because it is cheaper, reversible, and changes how
neither project runs its agent. Mode A is the deeper win and follows once
B has proven the envelope mapping.

## What Hermes gains

Each is a property arizuko enforces structurally, not a feature Hermes
could add cheaply: **persistence** (every message a row in `routd.db` with
FTS5 search, `5/C`) · **audit** (one `audit_log` row per state transition,
emitted in the mutation's own transaction via `audit.EmitInTx`, `5/I` —
not a droppable log line) · **grants** (`auth.Authorize` gates every tool
call on `(action, scope, params)` bound to the caller's folder, `5/32`) ·
**secrets injection** (folder- and user-scoped capability credentials
broker-resolved on the host at tool-call time, `5/13`, so they never sit
in the agent's process environment) · **tenancy** (the
folder coordinate is tenant, ACL, route, egress, web host, and file tree
at once — one Hermes deployment serves many tenants without Hermes
knowing tenancy exists) · **egress isolation** (per-folder rules through
crackbox).

## Token distribution

Six credential kinds, each with one job. This is the part an integrator
must get right; nothing else here is as easy to get subtly wrong.

| Credential                   | Minted by                             | Held by               | Authorizes                                                                                                          |
| ---------------------------- | ------------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `AUTHD_SERVICE_KEY`          | operator `.env`                       | authd, each daemon    | bootstrap — the right to ask for a real token. The last symmetric secret; `CHANNEL_SECRET` is retired               |
| `service:<daemon>` ES256 JWT | authd (JWKS-verified)                 | each daemon           | that daemon's own scopes, e.g. `messages:write`                                                                     |
| `service:proxyd` ES256 JWT   | authd                                 | proxyd only           | trust-stamping `X-User-*` inward; the pin is exactly `service:proxyd` and is load-bearing (`auth/middleware.go:14`) |
| route token                  | routd, opaque 256-bit, sha256 at rest | whoever holds the URL | append at **one** JID; `/chat/<t>/`, `/hook/<t>`, `/chat/<t>/mcp`                                                   |
| MCP socket                   | per folder, per turn                  | the spawned agent     | folder identity by socket path + `SO_PEERCRED`; tools gated by `db.Authorize(sub, folder, "mcp:"+tool, params)`     |
| user JWT                     | authd via OAuth                       | browser session       | operator/user scopes, folder-contained                                                                              |

Rules an integrator must not break:

- A daemon exchanges the bootstrap secret under its **daemon principal**
  (`AUTHD_SERVICE_NAME`, e.g. `teled`), never under a channel name — the
  mismatch 401s silently and outbound dies.
- A route token is append-at-one-JID. It is not a capability to be
  widened; a URL pasted on a website must never carry verb authority.
- Trust-stamped identity headers are trusted **only** behind the
  `service:proxyd` pin. Widening it to `service:*` would let any adapter
  assert an operator subject.

## Known gaps this integration inherits

Both must close before Mode A ships to a tenant that isn't the operator.

- **`Sender` is authorization-bearing — bound, but only by scheme.** A
  channel sender reaches `auth.Authorize`: `steer.go` passes `msg.Sender`
  to `db.IsOperator` → `Authorize(Caller{Principal: sub}, "admin", "**")`
  (`routd/sibling_db.go`), and a platform JID claims a canonical sub
  through an `acl_membership` edge (`auth/authorize_test.go`
  `TestAuthorize_JIDClaimViaMembership`). `handleMessages` rejects a
  sender outside a scheme the adapter registered (`Entry.OwnsScheme`,
  `4/1`). What that does NOT do is bound an edge to a sub-namespace
  _within_ its scheme, so a compromised adapter can still assert any user
  of its own platform. **An integration is trusted for its platform, not
  beyond it.**
- **Containment is per-handler, not per-store.** `BUGS.md` T1: the
  chat-token MCP `get_round` reads any turn in the instance because
  `store.TurnFrames` has no JID predicate while its HTTP twin checks one.
  Adding a second consumer of that surface multiplies the class.

**The turn is socket-credentialed, not token-credentialed.** The
`SO_PEERCRED`-gated socket is created at spawn and routd holds the other end
([`P-runed.md`](P-runed.md) § Capability brokering — REMOVED), so identity
is established by construction: Hermes authenticates by **being** the
spawned process and presents no credential. **Do not reintroduce a bearer
for Hermes** — a string handed to an agent with a shell and egress can be
copied out and replayed after the turn; the socket cannot.
