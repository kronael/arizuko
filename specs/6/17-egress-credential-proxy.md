---
status: shipped
shipped: 2026-08-20
source: paradigmxyz/centaur@20f3021 + paradigmxyz/iron-proxy (both Apache-2.0), read 2026-08-11
depends: [../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/17 — the opaque-client credential hole: DECLINED, all tiers

> **Operator decision 2026-08-20: none of this is ever to be built.** Credentials
> reach a client through the MCP broker or they do not reach it at all. This file
> is now a decision record, not a proposal. It stays because `6/19`, `6/23`,
> `6/24`, `6/25` and `SECURITY.md` cite the declines below; deleting it would
> orphan them.

## The hole, precisely (still true)

- **arizuko brokers only tools it dispatches.** A capability credential reaches a
  connector subprocess or a host-side REST call per call and is scrubbed from the
  result (`ipc/connector.go:120`, `ipc/extcall.go:59`). Container env never
  carries it (`5/14` §2, BUGS `X1` closed 2026-08-09).
- **The broker cannot reach a raw in-container client.** The egress proxy reads
  the destination off the request line and splices bytes; it terminates nothing,
  so it can neither see nor change anything inside the tunnel.
- **So an opaque client — `aws`, `gh`, `boto3`, a script the agent writes — has
  no path to a credential.** That is the gap `5/13` §"Out of scope" named `8/Z`.

## The decision

**That gap is closed by narrowing the promise, not by filling it.** arizuko
serves credentials through one mechanism: the MCP broker, per call, host-side,
authorized and audited. An opaque in-container client is **out of scope by
decision**. It is not "covered" — it is declined.

Why the narrowing is the right trade:

- Every alternative below moves a credential closer to the agent's process, and
  the four properties a broker call has (host-side credential, one authorization
  evaluator, HITL hold, audit row) are exactly what gets lost on the way.
- The one mechanism is testable, greppable, and already carries every real
  integration. A second mechanism would be a second thing to keep correct, for a
  case with no live consumer.
- `USELESS.md` scores us on precisely this axis. A credential in the agent's own
  process memory is a promise we would then have to make in the docs.

### Tier 0 — `credential_process`: DECLINED 2026-08-20

The proposal was to publish a credential source the SDK already asks for rather
than intercept the client: `credential_process` in `~/.aws/config` is an
arbitrary command returning JSON credentials on stdout, and `git`/`gh`/`docker`
have helpers of the same shape. The helper would ask the MCP unix socket —
already credentialed by `SO_PEERCRED` — for a short-lived credential.

It was cheap and it added no CA and no interception. It is still declined:

- **Its only named consumer was `X2`**, the `aws-devops` product, and **zero
  groups on krons, sloth or marinade use it** (verified 2026-08-20). Building a
  mechanism for a product nobody runs is speculative work.
- **Cooperating clients only.** A script doing `requests.get(url, headers=…)`
  gets nothing, so it never closed the general case it was named for.
- **The credential lands in the client's process memory**, readable by prompt
  injection inside that process — strictly weaker than the broker.

**Consequence:** `X2` closes as option (c), retire the product. The
`--product aws-devops` flag and `ant/examples/aws-devops/` promise per-engineer
AWS keys in container env that `X1` removed, so they fail silently and must go.

### Tier 1 — MITM and header injection: DECLINED 2026-08-18

Withdrawn, not deferred. The allowlist never needed MITM: the proxy filters on
the destination it reads off the request line and terminates nothing. Header
injection was therefore the only remaining reason to intercept TLS — you cannot
add a header to an HTTPS request without terminating it — and interception costs
a CA trusted inside every container, plus its lifecycle.

Nothing implements header injection today; verified 2026-08-18, the only match in
the tree is emaid defending _against_ it in SMTP.

### Vendor signing transforms: DECLINED 2026-08-15

iron-proxy's `aws_auth` / `gcp_auth` / `hmac_sign` transforms are declined. A
deliberate signed API call belongs in a host-side request tool where the
credential is read per call and never leaves the host — the agent picks _what to
ask for_, arizuko picks _how it is executed_. That generalises
`ipc.CallExtTool`; it is not an egress concern.

## Reopen trigger

A consumer that needs a credential inside an opaque client which has no
credential-helper hook AND cannot be expressed as a broker call. `X2` was not
that consumer. Absent one, the egress path stays MITM-free, which is also what
keeps the per-spawn proxy small enough to audit (`6/24`, ~250 lines, no TLS
termination, no CA).

## Attribution

Design derived from reading two Apache-2.0 repos: `paradigmxyz/centaur` @
`20f3021` and `paradigmxyz/iron-proxy`. No code was copied. Recorded here because
the reading is what produced the declines, not a build.

## Ties

`5/13` (the broker that IS the mechanism) · `5/14` (credential tiers) ·
`6/24` (the per-spawn proxy this keeps small) · `6/25` decision 1 (address
hygiene, which is not interception) · BUGS `X1` (env never carries a capability
credential) · `X2` (retire the product).
