---
status: draft
depends:
  [
    B-route-mode-ingestion,
    L-mention-promotion,
    G-engagement,
    E-routd,
    S-jid-format,
    5-tenant-self-service,
  ]
supersedes: []
---

# Onboarding model: bots, operators, staging groups

## Problem

How a new chat enters the system is scattered across five places: the
route table (`ROUTING.md`), `#observe` mode (`5/B`), onbod's admission
queue (`5/5`, `onbod/`), the ACL (`4/9`), and `arizuko group add`.
There is no canonical statement of the default posture, so the actual
default is **silence**: a new Telegram group the bot joins hits a route
miss and vanishes — stored in `routd.db`, visible to nobody. Witnessed
on krons 2026-07-09: `telegram:group/5567410596` posted for 40 minutes,
every message stored, zero trace surfaced; the operator had to grep the
DB for the JID (`BUGS.md` top entry).

This spec is a consolidation: the target model composes primitives that
already exist. One code gap (below) and doc reconciliation are the only
work.

## Actors

Every term is an existing primitive; none introduces new authz. In the
collapsed hierarchy of [`5/29`](29-worlds-guests-oauth.md) this spec is
**Tier 1**: onboarding admits a user to a **world** (the top-level group), and
promotion below gives a chat its own **agent** (a main group). `5/29` further
models auto-onboarding as a **Tier-3 session** scripted by an onboarding
prototype — a `5/29` delta, not current behavior: today the admission exchange
is a canned onbod message (`promptUnprompted`), not an agent run.

- **Bot** — a channel adapter identity: one adapter daemon + its
  platform credential (e.g. teled + a Telegram bot token, declared in
  `template/services/<daemon>.toml`). The bot defines **presence**:
  which chats the platform lets it see. It captures events and grants
  nothing. Its identity is configured (`AUTHD_SERVICE_NAME`,
  `CHANNEL_NAME`), never derived.
- **Operator** — emergent from a broad `acl` grant
  (`(role:operator, *, **, allow)` at bootstrap — `4/9`). Not a role
  column, no sentinel. Operators own the route table, promote staged
  chats, and issue invites.
- **Users authorized in a group** — folder-scoped authority, on two
  orthogonal axes (the `5/B` split: **mode controls firing; ACL
  controls visibility**):
  - _Channel side (firing)_: route rows with `sender=` / `verb=`
    predicates decide whose message fires a turn. Routing never
    consults the ACL.
  - _Authenticated side (access)_: `acl` rows (`interact` floor,
    `admin` above it — `4/9`) gate web/REST/dashboard access to the
    folder; a room JID may carry the audience baseline
    (`acl(room_jid, interact, folder)`).
- **Catch-all (staging) group** — a normal group designated as the
  default target for unsetup chats via a high-`seq` catch-all
  `#observe` route. Nothing distinguishes it in schema; it is a
  route-table convention.

## The model: a new group is a new channel

Telegram (and any group-capable platform) mimics Slack: the bot being
added to a group IS the channel appearing. No per-group wiring is
required to start capturing.

```
seq   match                     target
0     room=group/42             corp/board        ← promoted chat (specific)
9999  platform=telegram         staging#observe   ← catch-all staging
```

- **Capture**: the catch-all routes every un-promoted chat into the
  staging folder in observe mode — stored with `is_observed=1`, no
  turn, no reply (`5/B`). The bot stays silent in chats nobody set up.
- **Discovery**: the operator sees staged traffic where anything routed
  is seen — the staging folder's history (dashd, `inspect_messages`,
  the next trigger turn's `<observed>` window). The JID is in the row;
  no DB grep.
- **Precedence** is plain `seq` ordering, first match wins: promotion
  inserts a `seq 0` `room=` row that outranks the `seq 9999` catch-all.
  No flag, no special case.
- **Declaration** is route-table data, not code:
  `arizuko route <inst> add 'platform=telegram' 'staging#observe' --seq 9999`
  (equivalently `/v1/routes` REST, `routes` MCP, dashd — `5/16`). The
  staging folder is created like any group (dashd group-create,
  `/v1/groups`, `register_group`). One staging folder per platform or
  one shared — operator's choice via the `match` expression.
- **Not seeded**: `arizuko create` does not install a catch-all. A
  catch-all structurally disables chat-initiated onboarding for its
  platform (below) — that trade-off belongs to the operator, not a
  template default.

`web:` JIDs are out of scope: they address folders directly and never
consult the route table (`ROUTING.md` "Web JID model"); web admission
is route tokens (`5/W`).

## Promotion: staged chat → own folder

Promotion is the existing group-add, no new verb:

- `arizuko group <inst> add <jid> <folder>` — `SetupGroup` + group row
  - a `seq 0` `room=<room>` trigger route (`cmd/arizuko/main.go:361`).
    Discord guilds get the mention-only pair instead (mention trigger at
    `seq -1` + `#observe` at `seq 0`).
- Dashboard / REST: the same two resources (`groups`, `routes`) via
  resreg (`5/16`).
- Agent-driven: autoviv — a tier-1 agent with operator grant calls
  `register_group` (`template/web/pub/arizuko/concepts/autoviv.html`).

History does not move. Messages already stored under the staging folder
stay there (observed, operator-readable); messages after promotion
route to the new folder. No re-parenting, no magic.

## Private groups: who may drive the agent

In a private group, only authorized users fire turns; everyone else is
observed. This is sender-predicate stacking — the same shape as
mention-only channels, keyed on `sender=` instead of `verb=`:

```
seq   match                                        target
0     room=group/42 sender=telegram:user/7         corp/board
10    room=group/42                                corp/board#observe
```

Strict, not magical: an unauthorized sender matches only the observe
row — stored as context, no turn, no reply, no "helpful" fallback.
Combine predicates for mention-gated authorized users
(`sender=… verb=mention`).

Two scope notes, both existing semantics:

- **Engagement continues, predicates initiate.** An engagement window
  (`5/G`) is keyed on `(jid, topic)`, not sender, and overrides the
  route table. Once an authorized user engages the agent, any
  participant in that topic drives the conversation until TTL or
  `disengage()`. Sender rows gate who _opens_ a conversation, not who
  speaks inside one.
- **ACL is the other axis.** `4/9`'s `interact` gates the folder's
  authenticated surfaces and has no tier default (no grant → deny).
  Channel-side firing never consults it; do not add a second
  hand-rolled check in the loop — the route table is the firing gate.

## Two admission paths, reconciled

A route miss has exactly two outcomes, mutually exclusive per platform
**by construction** — the observe branch consumes the miss before the
onboarding branch runs (`routd/loop.go:505`–`533`):

1. **Staging catch-all (default posture)** — for deployments where the
   operator controls bot presence (invites the bot to groups). Silent
   capture, operator promotes. A platform with a catch-all can never
   reach the onboarding branch: no miss exists.
2. **Chat-initiated onboarding (opt-in, `ONBOARDING_ENABLED`)** — for
   public bots where strangers self-serve as new tenants. A genuine
   miss federates `POST /v1/onboarding` to onbod (owner of
   `onboarding`/`invites`/`onboarding_gates` in `onbod.db`); onbod
   greets the chat with an auth link (`promptUnprompted`), OAuth binds
   `user_sub`, gates queue or auto-approve (`matchGate`,
   `admitFromQueue` daily caps), and world creation runs `SetupGroup`
   then writes group + admin `acl` + `seq 0` routes in one tx
   (`createWorldTx`).
   Detail: `5/5`, `onbod/README.md`. Note this path CREATES a world per
   admitted stranger, where `5/29` Tier 1 admits users INTO an existing
   world (`world_invite`/`world_members`); the two shapes are unreconciled
   (`5/29` open questions).

The paths compose per-platform: `ONBOARDING_PLATFORMS` allowlists which
prefixes may onboard, and the route table decides where misses can
still occur — e.g. `platform=telegram room=group/*` → staging catch-all
while Telegram DMs miss through to onboarding. Invites (`5/5`) are the
third, out-of-band entry: operator-issued, no route miss involved.

## What exists vs the one gap

Everything above composes shipped primitives: route table + `seq`
precedence + `match` predicates (`ROUTING.md`, `5/E`), `#observe`
(`5/B`), mention promotion (`5/L`), engagement (`5/G`), unified ACL
(`4/9`), onbod queue + gates + invites (`5/5`, `onbod/`),
`arizuko group add` / `arizuko route add` (`cmd/arizuko/`), groups +
routes as resreg resources (`5/16`), autoviv. The staging posture is
route-table data plus documentation — no schema, no daemon, no new
mode.

The tables this spec manipulates — `groups`, `routes`, `onboarding_gates`,
`invites` — ARE the cold-tier resources
[`5/16`](16-mcp-rest-unification.md) models (one owner DB + PK + scope,
idempotently replaceable) and [`5/8`](8-yaml-manifests.md) transports as
YAML; identical resource names across all three specs. This spec drives
them as onboarding flow, not a separate table set.

**The one code gap** (`BUGS.md` 2026-07-09): the onboarding branch at
`routd/loop.go:520` is never entered on a live telegram-group route
miss even with `ONBOARDING_ENABLED=true` wired to routd and
`l.onbod != nil` verified. End-state contract the fix must satisfy:

- A genuine miss (no route, no observe, no engagement, no direct
  address) on an eligible platform inserts **exactly one** onboarding
  row per JID (`jid` is the PK; re-posts are idempotent) and advances
  the cursor.
- A catch-all observe route consumes the miss — staging platforms never
  onboard (this ordering is the reconciliation above; the fix must not
  reorder it).
- Discord guild channels onboard only on `verb=mention`;
  `ONBOARDING_PLATFORMS` allowlist holds.

## Consolidates

Canonical umbrella for the onboarding narrative; the referenced specs
stay canonical for their mechanisms. Nothing is superseded:

- `5/B` — `#observe` semantics (firing/visibility split quoted here).
- `5/L`, `5/G` — mention promotion, engagement continuation.
- `5/E` — route-miss → onboarding hook in the loop.
- `4/9` — `interact`/`admin`/`**` vocabulary; operator emergence.
- `5/5` — invites, gates, tenant self-service phases.
- `5/29` — the World → Agent → Session collapse; admission here is its
  Tier 1.
- `ROUTING.md` — route-table syntax + the mention-only examples this
  generalizes to staging and sender gating.
