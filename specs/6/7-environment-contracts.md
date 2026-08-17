---
status: draft
depends:
  [
    1-adoption-interop,
    16-daemon-standalone-matrix,
    ../5/9-agent-capability-eval,
  ]
---

# specs/6/7 — the two environment contracts

arizuko publishes an **environment**, and the value is what the environment
gives you for free. That is `6/1`'s corrected thesis, and this is the promise
each layer makes.

Two environments exist today and neither is stated as a contract. They are
prose in root `CLAUDE.md`, and nothing fails when they are violated — compare
`TestResourceName_NoStringLiteral`, which IS a contract because breaking it
breaks the build.

| Environment   | Who occupies it             | Conformance                    |
| ------------- | --------------------------- | ------------------------------ |
| **Agent**     | `ant` today, `pi` next      | `anteval` platform case set    |
| **Component** | a daemon or sibling library | a probe against a live process |

## Why this document exists

Three roadmap items are blocked on it, all for the same reason — nothing
declares what occupying a slot requires:

- **A second backend.** `ARIZUKO_BACKEND` existed, was never emitted as `-e` in
  the spawn args, and died silently. Nothing said what a backend must provide,
  so nothing noticed it provided nothing.
- **The substrate change.** Warm-per-group containers rewrite the turn
  lifecycle, and `16/1`'s durable streams rewrite it again. Without a contract
  each rewrite is a guess about what may move.
- **The channel layer as a shippable part.** `chanlib` has zero domain imports
  (`6/16`); the only thing keeping it in-tree is that its envelope is
  "whatever routd accepts" rather than a written contract.

## The agent contract

### Identity and authority

- The turn is **credentialed by the socket** — `SO_PEERCRED` on a per-folder
  MCP unix socket. There is no token to steal, and no token to forge.
- Authority is the **granted tool set**, never a coordinate. Depth confers
  nothing (`5/33`).
- Three tiers, and the grant model already expresses all three — this spec adds
  no new vocabulary:
  1. **Reads** — `inspect_*`, `find_messages`. Advertised unconditionally,
     containment applied per call.
  2. **The seeded `role:member` floor** — the twelve messaging verbs (`reply`,
     `send`, `send_file`, `send_voice`, `post`, `edit`, `delete`, `forward`,
     `quote`, `like`, `dislike`, `repost`), migration `0023`.
  3. **Everything else** — explicit delegation: routing, `network_*`, `acl`,
     invites, token mint, `register_group`, scheduling.

An author may rely on tiers 1 and 2 existing in every folder, and must degrade
gracefully without tier 3. A backend relies on none of it: the tool list is
discovered at runtime, so a backend implements the protocol, not the tools.

### Home

`/home/node/` persists across turns. `~/public_html` and `~/private_html` are
the two web slots; `~/diary`, `~/facts`, `~/users`, `~/workspace` and
`~/.claude` are never served at any URL. Anything outside the home is lost when
the turn ends.

### The envelope

The inbound payload IS the contract, not an implementation detail of `ant`:
`<messages>` with `<reply-to>` and `<attachment>` siblings, plus `<autocalls>`,
`<persona>`, `<observed>`, `<topic>` and `<link-context>`. A second backend
parses this. Defining a narrower payload for backend two would be a second wire
format for one consumer — the duplication this codebase spends its effort
deleting.

### Delivery — idempotent everywhere, not just at the end

**Every message and every MCP call carries the guarantee.** This is the part
most easily got wrong, and getting it wrong produces a contract with a special
case that does not belong in it:

- every inbound message has a stable id, and that id **is** the idempotency key
  (`routd/server.go` collapses `X-Idempotency-Key` into `messages.id`);
- every outbound send claims its key before the platform call and settles after
  (`chanlib.deliverOnce`), so a send is applied **at most once** and an
  unsettled claim is refused rather than guessed;
- an agent-authored frame dedups on its own key (`turn_results`).

**The terminal frame is not special here.** It looks special because the SDK
and `pi` treat "the turn is finished" as a distinct event in their own control
flow — that is a property of those harnesses, not of arizuko. In this contract
a terminal is simply the last frame, subject to the same rule as every other:
delivered at least once, applied at most once.

### The lifecycle seam — deliberately not frozen

Two roadmap items rewrite how a turn starts and ends. The contract therefore
states only what survives all three shapes:

- a turn is credentialed by the socket;
- it carries a stable key end to end;
- the agent appends frames;
- every frame is delivered at least once and applied at most once.

Per-turn ephemeral containers satisfy this today. A warm container serving N
turns satisfies it. A durable event log with replay satisfies it — replay is
re-reading frames whose application already happened. **Durable turns are an
implementation of this contract, not a break of it.** The version bumps only if
one of those four sentences becomes false.

### Conformance: `anteval`, per backend

A backend is arizuko-compatible to the degree it passes the platform case set
(`5/9`, 34 cases, `smoke = true` marking the gate basis). This replaces a prose
list of required-versus-optional behaviours: what is required is what the cases
assert, and the score is the compatibility statement. `pi` is the intended
second occupant, so the contract is written against a real second backend
rather than a hypothetical one.

## The component contract

What a daemon gets, and what it must provide in return. Every item ships
already — this is a statement of what is true, not a proposal.

| Provided                            | Required                                                                |
| ----------------------------------- | ----------------------------------------------------------------------- |
| a per-owner DB directory            | own and migrate your schema; touch no other DB                          |
| compose generation + env            | listen on `:8080`, always                                               |
| `obs` (audit_log / journald / OTLP) | `defer obs.Setup(name, instance)()` in `main()`                         |
| `resreg` two-face registration      | a management entity is a resource, not a bespoke tool                   |
| an injected authz `Gate`            | bind every `jid`/`folder`/`run_id` param to the caller's folder         |
| a compose fragment slot             | ship `template/services/<name>.yml`; no edit to proxyd or the generator |
|                                     | mount `GET /openapi.json`, public, before auth                          |
|                                     | write an `audit_log` row in the mutation's own tx                       |

**Conformance is a probe, not a checklist.** An in-repo daemon is checked by a
Go test; anything out-of-repo can only be checked against a running process.
**Shipped 2026-08-17 as `arizuko probe <base-url>`** (`cmd/arizuko/probe.go`),
so this section is no longer documentation:

| Check                                  | What a failure means                                                                    |
| -------------------------------------- | --------------------------------------------------------------------------------------- |
| `openapi.json` answers unauthenticated | it is mounted behind auth, so nothing can discover the component                        |
| the document is OpenAPI 3.x            | not a contract any consumer can read                                                    |
| every advertised path is served        | the doc names a resource the mux does not mount — a published lie                       |
| non-public paths require auth          | a `/v1` resource ships ungated                                                          |
| a foreign folder is refused            | the handler never binds its folder param to the caller (`--token` + `--foreign-folder`) |

**What the probe cannot see, stated so a pass is not over-read**: it detects
paths the doc advertises and the mux does not serve, never the reverse.
Deriving the mounted SET by probing is the mistake `F33` records — proxyd
answers every path from a `/` catch-all, so "it responded" never proves "this
resource is mounted here". That direction stays a Go test
(`resregtest.AssertServesNoForeignResource`).

An **empty** document is conforming, not a failure: `dashd` mounts only
`/dash/` pages and owns no `/v1` resource, so it truthfully advertises nothing.
A component owes an honest doc, not a non-empty one — the probe's first live run
failed `dashd` on that and the check was corrected, not the daemon.

The audit-row half of this table is not yet probed: it needs the component to
expose its own `audit_log` as a resource, which is not required of it today.

## Out of scope

The tool list itself (grants already express it), per-vendor credential
mechanics (`6/17`), and the substrate's reap policy (its own decision). This
spec says what must stay true across those, not how any of them works.

## Ties

`6/1` adoption thesis · `6/16` the four layers and where `ant` sits ·
`5/9` anteval · `5/33` grants · `5/P` runed · `5/34` channel protocol ·
`16/1` durable streams.
