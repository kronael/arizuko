---
status: draft
depends: [22-rust-track]
---

# specs/7/23 — the reframing: arizuko as a habitat for agents

The record of a design session, kept so the argument does not have to be
had twice. Nothing here is a change to make; it is the shape the system
already has, named, plus what is genuinely missing and what the outside
world already calls these things.

## The thesis

arizuko is a mediating layer with three verbs and one property:

1. **Ingest** — services normalize the world into messages.
2. **Route** — where a message goes; the folder is the tenancy coordinate.
3. **Grant** — what the agent may reach once it is there.

and the layer **meters and asserts** on everything crossing it —
`cost_log`, `audit_log`, `acl`, `hold:mcp:`, the egress allowlist. Those
are not separate subsystems. They are one layer, observed.

The property that explains the rest: **the turn is ephemeral.** Agents
are spawned, run and die. That is why routing must decide before anything
exists to ask, and why grants attach to a folder rather than to a
session. A long-lived agent would let both decisions be deferred; much of
what looks incidental exists only because they cannot be.

## Two standards, deliberately asymmetric

One canonical shape for what comes IN (`chanlib.InboundMsg`). One
canonical form for what goes OUT: a named, individually permissionable
tool the model chose to call.

Three properties force the asymmetry, and they are independent:

- **Routing is uniform.** Folder containment, thread inheritance and
  mention promotion are platform-independent, so one inbound shape means
  one implementation of each rather than N.
- **Actions genuinely differ.** A Telegram button row, a Reddit upvote
  and a GitHub review are not one act in different clothes. One `send()`
  either flattens to the lowest common denominator or grows a union type
  nobody can read.
- **Ingress is mechanical; egress is a decision.** A message arrived —
  route it. Whether to reply, react, open an issue or stay quiet is
  exactly what tool-calling is for.

## What the outside world calls this

Researched 2026-09-05. The split is common in shape and named three
times, in none of them for agents:

| Vocabulary                       | Name                              |
| -------------------------------- | --------------------------------- |
| Cockburn, hexagonal architecture | driving vs driven adapters        |
| Enterprise Integration Patterns  | Normalizer + Canonical Data Model |
| Industry (Zapier, n8n, Composio) | triggers vs actions               |

MCP's own specification names only the egress half — tools are
"model-controlled" — and defines no server-initiated inbound feature at
all.

**One OSS project ships the exact combination**: `letta-ai/letta-code`,
whose `MessageChannel` tool documentation states that a normal assistant
response is not delivered back to the channel automatically.
`openclaw` has it as a per-turn mode; Claude Code's channels reference
states it as a contract.

**Nobody pairs it with tenancy.** Systems with a real permission boundary
(Dust, Onyx, Chatwoot) are all symmetric — the connector posts back
directly. Nothing found has folder-depth grants applied by one gate to
both MCP and REST.

Verdict: no adoptable router exists. Two things are worth reading for
parts — mautrix `bridgev2`'s event taxonomy and `PortalKey` identity
type, and n8n's `connect_credentials` per-caller credential gate.

## The permission gradient

The `acl` row is `principal | action | scope | effect | params |
predicate | granted_by | granted_at | grant_option`. Against IAM's
Principal / Action / Resource / Effect / Condition that is the same tuple
plus `grant_option` — SQL's `WITH GRANT OPTION` — which is delegation
stated directly rather than through a permission boundary.

The zero-config-to-enterprise gradient is already visible in live rows:

- `role:operator | * | ** | allow` — the whole small-deployment configuration.
- `role:member | mcp:reply | ** | allow` — the floor every group inherits.
- `folder:X | web_route.set | X | allow` — one narrow capability on one folder.

A single user configures nothing. An enterprise writes the third shape as
deep as it needs. One evaluator serves all three.

## Correction worth keeping

Putting egress behind MCP does NOT move the platform credential into the
broker. routd's tools delegate to the adapter's `/send`, so the token
stays in the adapter; the tool holds the **authority** (the grant gate),
not the credential. Composio and Arcade are the products where the tool
server holds the credential. The agent holds it in neither case, but the
distinction matters when comparing designs.

## Open threads

- `webd` registers as the `web` channel (`webd/main.go:127`). `hook:` is
  the scheme that does not register — it is delivered by source name
  (`routd/deliver.go:55-64`), which is consistent with it being the
  capability-token door for senders that cannot authenticate rather than
  a channel.
- `HasCap` is consulted in `chanreg/httpchan.go` (18 sites) when folding
  options, and never exposed to the agent, so a model learns a channel's
  limits by failing rather than by looking.
- Tenancy rides `route_tokens.target_folder` as well as the JID, and the
  token target is read first (`routd/loop.go:762`).
- Every shipped package manifest in `template/services/` declares only
  `proxyd_routes`. Whether the apply engine supports more sections than
  any package uses was not established.

## Ties

`22-rust-track` · `21-storage-seam` · `6/16` daemon standalone matrix ·
`5/33` paths and roles (the grant model) · `5/34` channel protocol.
