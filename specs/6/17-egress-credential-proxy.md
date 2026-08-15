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

## Tier 1 — MITM for the genuinely opaque client (gated on a consumer)

What remains is the client that speaks neither MCP, nor the request
tool, nor a helper protocol. The proxy is justified for that residue
only, and the residue shrinks with every capability that becomes a tool.

### Decline the signing transforms — split by auth mechanism

**Adopt iron-proxy's `allowlist` and header injection. DECLINE
`aws_auth`, `gcp_auth` and `hmac_sign`.** Taking them means a
network-policy component owns cryptographic request signing for N
clouds: every new provider changes the box on the critical path for ALL
egress, and SigV4's edge cases (STS session tokens, chunked payloads,
unsigned-payload mode) are ours to track — where subtly wrong means
either broken requests or a signature covering the wrong bytes. It also
crosses `6/16`'s component line: a component owns MECHANISM and takes
strings, and a proxy implementing AWS's auth protocol owns vendor
DOMAIN.

The fork is genuine and has no free option:

- key out of the client → something in the path must sign → it needs
  every vendor's crypto;
- signing done by code that already owns the protocol → the **SDK**
  signs → the SDK needs the key.

So split by AUTH MECHANISM, not by client type:

| Auth shape                        | Where the credential lives | Mechanism                                |
| --------------------------------- | -------------------------- | ---------------------------------------- |
| Bearer / API key (most HTTP APIs) | the proxy                  | header injection, bound to the host      |
| Request-signing (SigV4, HMAC)     | the client, short-lived    | SDK signs; `credential_process` (tier 0) |

The proxy stays dumb — allowlist plus header injection — and vendor
crypto stays where the vendor maintains it. TLS termination is still
required to inject a header into an HTTPS request, so MITM remains;
what goes away is carrying AWS's, Google's and Azure's signing code
inside the egress chokepoint.

### The design, and who ships which half

- **Data plane: `paradigmxyz/iron-proxy`** (Apache-2.0, Go; the
  `ironsh/iron-proxy` URL redirects there). A MITM egress proxy with a
  built-in DNS server. It terminates TLS with leaf certificates it
  signs on the fly, from a CA the operator provides; its transform
  pipeline is ordered and default-deny. arizuko uses `allowlist` (403
  outside the list), `header_allowlist`, and `secrets` (replace or
  inject a credential; sources `env`/`file`/… — no inline YAML value);
  the vendor-signing transforms it also ships are declined above. A
  management API (`POST /v1/reload`, bearer key via
  `IRON_MANAGEMENT_API_KEY`) re-reads the config file and swaps the
  pipeline atomically; a bad config returns 422 and keeps the old
  pipeline. Reference: `docs.iron.sh/reference/configuration`.
- **Control plane: arizuko's own.** Centaur renders per-sandbox proxy
  config from `tools.toml` declarations
  (`services/console/app/models/principal_sync_config_snapshot.rb`).
  arizuko renders from **grant rows plus the credential store**. Our
  policy source is the identity model we already have; that is the
  reason to own this half.

The load-bearing idea: **the tool holds a placeholder; the proxy holds
the value.** A leaked placeholder is useless because substitution is
bound to a destination host. In inject mode the client never sees any
value (`tools/research/websearch/pyproject.toml:36`).

### Control-plane stances arizuko sets (Centaur sets them differently)

1. **Fail closed.** iron-proxy's `secrets` rule has a `require` field,
   default `false`; with `true`, a request to a matching host without
   the proxy token is rejected with 403. Centaur hardcodes
   `require: false` on the tool path (`centaur-perms/src/translate.rs:191`),
   and its control plane silently omits a secret whose credential does
   not resolve (`services/console/docs/API.md:139`) — the placeholder
   then goes upstream as a literal (observed as real 401s,
   `centaur-sandbox-agent-k8s/src/iron_proxy.rs:61-73`). arizuko
   inverts both levers: every rendered rule sets `require: true`, and
   the renderer refuses to render a granted host whose credential does
   not resolve — the turn fails loudly instead (CLAUDE.md §"Fail loud").
2. **No wide-open seed.** Centaur's checked-in seed config allowlists
   `domains: ["*"]` — fully open, with the real policy pushed at
   runtime (`services/iron-proxy/iron-proxy.yaml:22-25`). arizuko's
   renderer writes the folder's real allowlist into the file; a sidecar
   that has not been configured allows nothing.
3. **One credential store, two injection sites.** The renderer reads
   `store.FolderSecretsResolvedForUser` (`store/secrets.go:547`) — the
   same reader the `5/13` broker uses. Never a second credential table.
   iron-proxy has no inline-value source, so the renderer writes each
   value to a `0600` file in the sidecar's private volume and points a
   `file` source at it.

### The delta (if tier 1 ships)

- **One iron-proxy sidecar per folder network**, pinned by version AND
  digest (Centaur pins `0.49.0@sha256:c462…`,
  `services/iron-proxy/Dockerfile:3`; arizuko pins its own). Per-folder
  because iron-proxy runs one pipeline per process and has no
  per-source-IP registry (crackbox's model): a shared instance would
  inject folder A's credential into folder B's request to the same
  host. Centaur likewise runs it per sandbox.
- **Render + reload before each spawn.** routd renders the config per
  turn from grants + store (the triggering user's keys overlay folder
  defaults, `5/14` §"Who is the caller"). The spawn path writes the
  file and the secret files, POSTs `/v1/reload`, and starts the agent
  container only on 200 — any other answer aborts the spawn. The
  per-folder run slot (`runed/audit.go`) serializes turns, so a reload
  cannot race a same-folder turn.
- **CA lifecycle.** `arizuko create` generates one CA per instance. The
  key mounts only into sidecars; the certificate mounts into agent
  containers. Spawn env sets `NODE_EXTRA_CA_CERTS`,
  `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`,
  `GIT_SSL_CAINFO` (Centaur's set, `iron_proxy.rs:1687-1703`). Known
  gap, stated: rustls, Deno, and JVM clients ignore these variables.
  Rotation is an operator command, not automatic.
- **crackbox is replaced on the agent egress path.** The sidecar covers
  the allowlist, the DNS filter (arizuko never wired crackbox's `6/10`
  filter — `6/10` §Scope records that `--dns` was never passed), and
  adds the injection crackbox declined. `container/egress.go` switches
  from crackbox register/unregister to sidecar render/reload. Running
  both planes is rejected — two egress paths drift. crackbox stays a
  shipped standalone component (`6/8`) with external consumers, and
  `crackbox/pkg/host` (`6/9`, KVM) is untouched; arizuko simply stops
  consuming its proxy. Note also: iron-proxy's MCP tool allowlist
  overlaps `6/12` (mcpfw, draft, unbuilt) — evaluate it before building
  mcpfw.

### Cost

- A vendored, digest-pinned upstream image; release review on every bump.
- A CA lifecycle (generate, mount, rotate) — a new operational surface.
- The config renderer, reload plumbing, and one sidecar container per
  folder (today: one shared crackbox per instance). Container count and
  RAM grow with folder count.
- A decrypting middlebox on the egress path. A bug there is a
  wrong-credential-to-wrong-host bug. Tests must assert that a
  placeholder never reaches an upstream and that a request from one
  folder never matches another folder's rule.

## Tier placement (`5/14`)

The request tool, the helper, and the proxy are all **injection sites
for capability credentials (type 2)**; the first site is the `5/13`
broker. Env-profile keys (type 1) keep their spawn-env path unchanged.
Infra credentials (type 3) stay in the host `.env`; the sidecar's
management key is type 3. No new tier.

## Recommendation

**Ship tier 0 first** — it closes `X2` for a fraction of the cost and
adds no new trust surface. The request tool carries the signing cases.
Adopt the MITM data plane only for opaque clients, gated on a consumer
that tier 0 cannot serve. Do not build the CA lifecycle without one;
`X2` no longer counts, because tier 0 covers it.

## Attribution

Design derived from reading two Apache-2.0 repos: `paradigmxyz/centaur`
@ `20f3021` (`services/iron-proxy/`, the `centaur-iron-*`/`centaur-perms`/
`centaur-sandbox-agent-k8s` crates, `services/console/`,
`tools/*/pyproject.toml`) and `paradigmxyz/iron-proxy` (README +
`docs.iron.sh`). No code was copied. An earlier revision wrongly
recorded the data plane as closed and unread (corrected 2026-08-11);
a later revision proposed adopting the full transform pipeline,
including vendor signing (narrowed 2026-08-15).
