---
status: draft
depends: [22-rust-track, 25-publish-plan]
---

# specs/7/23 — the reframing: arizuko as a habitat for agents

The record of a design session, kept so the argument does not have to be
had twice. Nothing here is a change to make; it is the shape the system
already has, named, what the outside world already calls these things,
and which of them a stranger can adopt off the shelf.

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

## The participant shape — a package

The three verbs say what the layer does. They do not say what may speak
to it. That shape is settled (2026-09-05): **a package — a process plus
a declaration of how it hooks into the system** (`7/25`). It is not a
fourth verb. It is what every participant is, wherever its source lives:
an adapter that ingests, a gateway the turn's model calls go through, a
proxy a spawn's traffic leaves by. Hurd's translator has the same shape —
a normal program attached to a node with `settrans`, speaking that node's
interface — and the maintainer's framing is "mostly like Hurd, but this
one should work".

The declaration hooks into the verbs by name. A channel registration
(`POST /v1/channels/register`) is the ingest hook; `proxyd_routes` and
`routes` rows are the route hook; `acl` rows, bounded to the folder the
operator named at install, are the grant hook. The ephemeral turn is why
every hook is a row or a registration and never a callback into a
process: nothing long-lived exists to be called back.

## What the outside world calls this

Researched 2026-09-05 against primary sources. The split is common in
shape and named three times, in none of them for agents:

| Vocabulary                       | Name                              | Source                                                                                   |
| -------------------------------- | --------------------------------- | ---------------------------------------------------------------------------------------- |
| Cockburn, hexagonal architecture | driving vs driven adapters        | alistair.cockburn.us/hexagonal-architecture                                              |
| Enterprise Integration Patterns  | Normalizer + Canonical Data Model | enterpriseintegrationpatterns.com/patterns/messaging/Normalizer.html                     |
| Industry (Zapier, n8n, Composio) | triggers vs actions               | help.zapier.com 8496181725453 · docs.n8n.io node-types · docs.composio.dev/docs/triggers |

MCP names only the egress half: tools are "model-controlled"
(modelcontextprotocol.io/specification/2025-06-18/server/tools; the
current revision is 2025-11-25). The protocol does carry server-initiated
requests — `sampling/createMessage`, `elicitation/create`, `roots/list`,
the `list_changed` and `resources/updated` notifications, and the
2025-11-25 experimental tasks — but every one runs inside a session the
client already opened. None starts a turn from an external event. The
trigger half has no MCP primitive.

## The asymmetry is not rare; the pairing is

Three OSS projects ship the exact ingress/egress split natively, and a
fourth states it as a contract:

| Project                        | License    | Where it says so                                                                                                                                                                                                                                                       |
| ------------------------------ | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `letta-ai/letta-code`          | Apache-2.0 | `src/channels/README.md`: "A channel message can successfully reach the agent, but the agent still has to call `MessageChannel` to reply."                                                                                                                             |
| `openclaw`                     | MIT        | `docs/channels/groups.md` (main, 2026-09-05): `messages.groupChat.visibleReplies: "message_tool"` is an opt-in mode; the default is `"automatic"`, the mode falls back to automatic when the tool is unavailable, and command turns and plugin-bound threads bypass it |
| `yc-software/qm`               | MIT        | `src/core/orchestrator/surface-tools.ts`: `post` `reach` `react` `edit` `delete` plus `staySilent`; nothing posts without a call                                                                                                                                       |
| Claude Code channels reference | —          | code.claude.com/docs/en/channels-reference: "the channel contract: capability declaration, notification events, reply tools …"                                                                                                                                         |

So "a model's plain reply is not delivered" is a design pattern with
public precedent, not an arizuko invention. Do not claim it as one. What
arizuko has and openclaw's mode does not is the absence of any
auto-delivery path — no fallback, no command bypass, no plugin bypass.
That is the defensible sentence. (`refs/` clones lag upstream by months;
the openclaw row is cited from live `main`, never from `refs/`.)

What none of the four has is a **permission scope with depth**. letta-code
gates by sender allowlist per account (`dmPolicy`, `allowedUsers`,
`admin_users`), and the server under it authenticates with one shared
password (`SECURE=true`, `LETTA_SERVER_PASSWORD`;
docs.letta.com/guides/selfhosting) — no accounts, no tenants. qm's scope
is a closed, flat set of kinds (`personal | channel | team | org |
group`), so it cannot express `corp/eng/sre/oncall`. openclaw's operator
scopes are, by its own docs, "not hostile multi-tenant isolation"
(`docs/gateway/operator-scopes.md`). Hermes delivers replies "without
special handling from the agent" — symmetric.

Systems with a real permission boundary are symmetric: Dust's Slack
connector streams the answer itself (`connectors/src/connectors/slack/bot.ts`,
`streamConversationToSlack`); Onyx's bot posts it
(`backend/onyx/onyxbot/slack/handlers/handle_regular_answer.py`,
`respond_in_thread_or_channel`); Chatwoot's own Captain posts it
(`enterprise/app/jobs/captain/conversation/response_builder_job.rb`,
under its proprietary `enterprise/` license), and its bring-your-own
AgentBot is a webhook in and a REST call out — a helpdesk, not an agent
runtime. Their scopes are flat: workspace, account, inbox.

The gate itself has one near neighbour. `paperclipai/paperclip` (MIT)
runs one policy engine — `allow | deny | require_approval | rate_limited
| defer_runtime` — for the agent's `POST /api/tool-gateway/tools/call`
and the human's `…/action-requests/:id/approve`
(`doc/MCP-ACCESS-GOVERNANCE.md`): "one gate, two faces", shipped. Its
scope is a fixed five-level lattice, `issue > routine > agent > project >
company`, not a path, and it has no chat ingress (issues #7, #49). And
in the field the two halves exclude each other inside one product:
AnythingLLM's Telegram connector "only works in single-user mode …
Enabling multi-user mode will automatically disconnect the bot"
(docs.anythingllm.com/channels/telegram).

Verdict: no adoptable router exists. The asymmetry is adoptable from
three projects, the gate from Paperclip, the coordinate from Keycloak
(`6/16`); the binding of one path to tenant, grant, route, egress, web
host and file tree, applied by one gate to both MCP and REST, is not
shipped anywhere found. The field by property is in `6/1`.

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

## The credential stays in the adapter

Putting egress behind MCP does NOT move the platform credential into the
broker. routd's tools delegate to the adapter's `/send`, so the token
stays in the adapter; the tool holds the **authority** (the grant gate),
not the credential. Composio and Arcade are the products where the tool
server holds the credential: Composio "injects the connected account's
credentials server-side" (docs.composio.dev/docs/connected-accounts) and
Arcade "will securely inject the OAuth token into the tool's Context"
(docs.arcade.dev, create-tool-auth). The agent holds it in neither case,
but the distinction matters when comparing designs.

## Ties

`25-publish-plan` (the package contract) · `22-rust-track` ·
`6/37` · `6/16` daemon standalone matrix · `6/1` adoption (the
field measured by property) · `5/33` paths and roles (the grant model) ·
`5/34` channel protocol.
