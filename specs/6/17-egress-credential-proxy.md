---
status: draft
source: paradigmxyz/centaur@20f3021 + paradigmxyz/iron-proxy (both Apache-2.0), 2026-08-11
depends: [8-crackbox-standalone, ../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/17 — the opaque-client credential hole

Closes the one credential hole arizuko still has: an **opaque HTTP
client inside the container** — the `aws` CLI, `gh`, `boto3`, or any
script the agent writes. `5/13`/`5/14` closed every brokered path, but a
raw client has no broker to hook. `5/13` §"Out of scope"
(`specs/5/13-ext-mcp.md:163`) names this gap `8/Z`, never written. This
is it.

Two operator decisions (2026-08-15) narrowed an earlier adopt-the-MITM
plan to the tiered shape below: **signing rides a host-side request
tool, not the egress path**, and the residual proxy is **allowlist +
header injection only** — iron-proxy's `aws_auth`/`gcp_auth`/`hmac_sign`
transforms are declined.

## The hole, precisely

- **arizuko brokers only tools it dispatches.** A capability credential
  reaches a connector subprocess or a REST request on the host, per
  call, and is scrubbed from the result (`ipc/connector.go:120`,
  `ipc/extcall.go:59`). The container env never carries it (`5/14` §2,
  BUGS `X1` closed 2026-08-09).
- **The broker cannot reach a raw in-container client.** crackbox
  tunnels HTTPS by splicing raw bytes (`crackbox/pkg/proxy/proxy.go:95,159`)
  after it peeks only the SNI (`crackbox/pkg/proxy/peek.go`). It cannot
  see or change anything inside the tunnel; response scanning is
  declined (`specs/6/8-crackbox-standalone.md:138`).
- **The only remaining way to feed such a client is container env** —
  which `X1` forbids. BUGS `X2` is the live casualty: the aws-devops
  product needs per-engineer `AWS_*` keys inside `boto3` for CloudTrail
  attribution, and `X1` removed the mechanism.

## Signing rides a request tool, not the egress path (2026-08-15)

**Signing belongs in a host-side MCP tool that performs the request.**
The agent calls a generic request tool — target, method, body, and an
auth scheme naming a credential (`sigv4:aws_prod`, `hmac:partner_api`,
`bearer:github`). `ipc.CallExtTool` (`ipc/extcall.go:59`) already makes
host-side REST calls with secrets resolved per call; the generalization
is the scheme set plus letting the caller name the request rather than
only pre-declared endpoints. The credential is read from arizuko's own
secret store (`store.FolderSecretsResolvedForUser`,
`store/secrets.go:547`), used to sign in routd's process, and never
leaves the host.

**Why this is safe where a generic `run(command, credential)` is not** —
the distinction is what the agent controls:

| Shape                                 | Agent controls        | Platform controls | Safe                                           |
| ------------------------------------- | --------------------- | ----------------- | ---------------------------------------------- |
| `run(command=…, credential=…)`        | execution environment | nothing useful    | no — `echo $TOKEN \| base64` defeats scrubbing |
| `request(url=…, auth=sigv4:aws_prod)` | the request           | execution + value | yes — there is no shell and no env to read     |

The agent never receives the credential and has no process in which to
observe it. It picks _what to ask for_; arizuko picks _how it is
executed_.

**What this buys over a signing proxy**, for the same case:

- All four tool-call properties (`SECURITY.md` §"Why a tool call beats a
  raw socket"): host-side credential, one authorization evaluator, HITL
  hold, audit row. A proxy gives only the first.
- Per-request containment is already expressible — an `acl` row carries
  `params`, so `params=host=api.github.com` scopes the grant without
  inventing HTTP policy.
- No CA, no TLS termination, no vendor crypto in the egress path, no
  sidecar per folder network.

The integrations themselves are not arizuko's problem to enumerate: the
ecosystem is producing MCP servers for the third-party surface, and this
tool covers the long tail of private endpoints that will never get one.

## Tier 0 — the cooperating client (ships first, closes `X2`)

**Lambda does not intercept your HTTP client; it publishes a credential
source the SDK already knows to ask for.** That is a credential-helper
protocol, and the clients in question already speak one:

- the AWS CLI and **boto3** both honour `credential_process` in
  `~/.aws/config` — an arbitrary command returning JSON credentials on
  stdout, called on demand;
- `git`, `gh` and `docker` have credential helpers of the same shape.

So the tool asks a local helper, the helper asks arizuko's MCP unix
socket — already credentialed by `SO_PEERCRED`, no token — and gets a
short-lived credential. **No MITM, no CA in the container trust store,
no TLS termination, no new high-value target.** It reuses the trust
boundary that already exists instead of standing up a second one.
Prefer a credential that expires on its own (an STS session token) so
what can leak has a bounded life.

Limits, stated plainly because they decide the tiering:

- **Cooperating clients only.** A script the agent writes doing
  `requests.get(url, headers={...})` has no helper protocol and gets
  nothing. MITM covers that case; this does not.
- **The credential lands in the client's process memory.** Prompt
  injection inside that process can read it — the exact axis
  `USELESS.md` scores us down on. Weaker than MITM; far stronger than
  container env, because it is per-use, auditable and revocable.

**`X2` is this spec's only named consumer, and `credential_process`
satisfies it exactly** — per-engineer `AWS_*` keys inside boto3 for
CloudTrail attribution, with no proxy at all.

## Tier 1 — MITM and header injection: DECLINED (2026-08-18)

Withdrawn, not deferred. The earlier text specced a MITM data plane whose
residue after the 2026-08-15 narrowing was "allowlist + header injection".

**The allowlist never needed MITM.** crackbox filters by peeking the SNI and
then splicing raw bytes (`crackbox/pkg/proxy/peek.go`), terminating nothing. So
**header injection was the only remaining reason to intercept TLS** — you cannot
add a header to an HTTPS request without terminating it — and interception costs
a CA trusted inside every container, plus its lifecycle.

What that CA would have bought: coverage for a client that speaks neither MCP,
nor the request tool, nor a credential helper — `gh`, `curl`, a script the agent
writes. What already covers the real cases:

- **tier 0 (`credential_process`)** closes `X2`, the live casualty, for `aws`
  and `boto3`;
- **the host-side request tool** carries every deliberate signed API call;
- **`L49`'s gateway** removes the model key from container env without any
  interception, because a gateway is configured rather than intercepted.

So the residue is narrow and the price is a fleet-wide interception capability.
Declined. Nothing implements header injection today — verified 2026-08-18, the
only match in the tree is emaid defending _against_ it in SMTP.

**Reopen trigger**: a consumer that needs a credential inside an opaque client
which has no credential-helper hook AND cannot be expressed as a request-tool
call. Absent that, the egress proxy stays MITM-free, which is also what keeps
the per-spawn proxy in `L48` small enough to audit (~450 lines, no TLS
termination, no CA).

## Tier placement (`5/14`)

The request tool, the helper, and the proxy are all **injection sites
for capability credentials (type 2)**; the first site is the `5/13`
broker. Env-profile keys (type 1) keep their spawn-env path unchanged.
Infra credentials (type 3) stay in the host `.env`; the sidecar's
management key is type 3. No new tier.

## Recommendation

**Ship tier 0.** It closes `X2` for a fraction of the cost and adds no
new trust surface, and the request tool carries the signing cases. Tier 1
is declined above, so tier 0 plus the request tool is the whole of this
spec — there is no CA lifecycle to build and no interception to operate.

This spec has now been cut three times (2026-08-15 twice, 2026-08-18),
each cut removing something the egress path did not need. What survives is
small enough that folding it into `5/13` is worth considering: the signing
half generalises `ipc.CallExtTool`, and only `credential_process` is a
genuinely new mechanism.

## Attribution

Design derived from reading two Apache-2.0 repos: `paradigmxyz/centaur`
@ `20f3021` (`services/iron-proxy/`, the `centaur-iron-*`/`centaur-perms`/
`centaur-sandbox-agent-k8s` crates, `services/console/`,
`tools/*/pyproject.toml`) and `paradigmxyz/iron-proxy` (README +
`docs.iron.sh`). No code was copied. An earlier revision wrongly
recorded the data plane as closed and unread (corrected 2026-08-11);
a later revision proposed adopting the full transform pipeline,
including vendor signing (narrowed 2026-08-15).
