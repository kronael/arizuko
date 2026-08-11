---
status: draft
source: paradigmxyz/centaur@20f3021 + paradigmxyz/iron-proxy (both Apache-2.0), 2026-08-11
depends: [8-crackbox-standalone, ../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/17 — egress credential proxy (the opaque-client hole)

Proposal to close the one credential hole arizuko still has: an
**opaque HTTP client inside the container** — the `aws` CLI, `gh`,
`boto3`, or any script the agent writes. `5/13`/`5/14` closed every
brokered path, but a raw client has no broker to hook. `5/13` §"Out of
scope" (`specs/5/13-ext-mcp.md:163`) names this gap `8/Z`, never
written. This is it.

Plan: **adopt the iron-proxy data plane, own the control plane.** An
earlier draft claimed the data plane was closed and proposed to build
MITM inside crackbox. That was wrong — `github.com/paradigmxyz/iron-proxy`
is open, Apache-2.0, Go — and the build-it plan falls with it.

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

## The design, and who ships which half

- **Data plane: `paradigmxyz/iron-proxy`** (Apache-2.0, Go; the
  `ironsh/iron-proxy` URL redirects there). A MITM egress proxy with a
  built-in DNS server. It terminates TLS with leaf certificates it
  signs on the fly, from a CA the operator provides. Its DNS server
  resolves every name to `proxy_ip`, with passthrough domains and
  static records. Its transform pipeline is ordered and default-deny:
  `allowlist` (403 outside the list), `header_allowlist`, `secrets`
  (replace or inject a credential; sources `env`/`file`/`aws_sm`/
  `aws_ssm`/`1password` — no inline YAML value), `aws_auth` (SigV4
  re-sign), `oauth_token`, `gcp_auth`, `hmac_sign`, audit transforms,
  and an MCP tool allowlist. A management API (`POST /v1/reload`,
  bearer key via `IRON_MANAGEMENT_API_KEY`) re-reads the config file
  and swaps the pipeline atomically; a bad config returns 422 and keeps
  the old pipeline. Reference: `docs.iron.sh/reference/configuration`.
  (The README omits `aws_auth`; the configuration reference has it.)
- **Control plane: arizuko's own.** Centaur renders per-sandbox proxy
  config from `tools.toml` declarations
  (`services/console/app/models/principal_sync_config_snapshot.rb`).
  arizuko renders from **grant rows plus the credential store**. Our
  policy source is the identity model we already have; that is the
  reason to own this half.

The load-bearing idea survives adoption unchanged: **the tool holds a
placeholder; the proxy holds the value.** A leaked placeholder is
useless because substitution is bound to a destination host. In inject
mode the client never sees any value (`tools/research/websearch/pyproject.toml:36`).

## Control-plane stances arizuko sets (Centaur sets them differently)

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
   `store.FolderSecretsResolvedForUser` (`store/secrets.go:543`) — the
   same reader the `5/13` broker uses. Never a second credential table.
   iron-proxy has no inline-value source, so the renderer writes each
   value to a `0600` file in the sidecar's private volume and points a
   `file` source at it.

## The delta

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
  container only on 200 — any other answer aborts the spawn.
  Reload-from-file is the documented update path; there is no file
  polling. The per-folder run slot (`runed/audit.go`)
  serializes turns, so a reload cannot race a same-folder turn.
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
  from crackbox register/unregister to sidecar render/reload; the
  per-instance crackbox container leaves the deployment. Running both
  planes is rejected — two egress paths drift. crackbox stays a shipped
  standalone component (`6/8`) with external consumers, and
  `crackbox/pkg/host` (`6/9`, KVM) is untouched; arizuko simply stops
  consuming its proxy. Retiring more of crackbox is a separate user
  decision. Note also: iron-proxy's MCP tool allowlist overlaps `6/12`
  (mcpfw, draft, unbuilt) — evaluate it before building mcpfw.

## `X2` is the consumer — satisfied by configuration

`aws_auth` is iron-proxy's own transform, not a Centaur addition
(`tools/infra/cloudwatch/pyproject.toml:30` rides it). It gives `X2`
option (a) with zero new crypto code: `boto3` signs with throwaway
placeholder keys inside the container; the sidecar re-signs with the
real per-user keys from the store, scoped by `allowed_services`.
CloudTrail attribution survives because the keys resolve per triggering
user, exactly as the broker resolves them. The real keys never enter
the container.

## Tier placement (`5/14`)

The proxy is a **second injection site for capability credentials
(type 2)**; the first is the `5/13` broker. Env-profile keys (type 1)
keep their spawn-env path unchanged. Infra credentials (type 3) stay in
the host `.env`; the sidecar's management key is type 3. No new tier.

## Cost

- A vendored, digest-pinned upstream image; release review on every bump.
- A CA lifecycle (generate, mount, rotate) — a new operational surface.
- The config renderer, reload plumbing, and one sidecar container per
  folder (today: one shared crackbox per instance). Container count and
  RAM grow with folder count.
- A decrypting middlebox on the egress path. A bug there is a
  wrong-credential-to-wrong-host bug. Tests must assert that a
  placeholder never reaches an upstream and that a request from one
  folder never matches another folder's rule.

Against the earlier build-it plan, adoption deletes: a TLS-termination
path in crackbox, leaf minting and caching, SigV4 re-signing crypto,
and every transform executor. What remains to build is a renderer, file
writes, and one HTTP call.

## Recommendation

Adopt, gated on `X2`'s sign-off — this is `X2` option (a) by
configuration. If the user picks (c) — retire aws-devops — this spec
shrinks to "nice to have". Do not build the CA lifecycle without a
consumer; `X2` is the consumer.

## Attribution

Design derived from reading two Apache-2.0 repos: `paradigmxyz/centaur`
@ `20f3021` (`services/iron-proxy/`, the `centaur-iron-*`/`centaur-perms`/
`centaur-sandbox-agent-k8s` crates, `services/console/`,
`tools/*/pyproject.toml`) and `paradigmxyz/iron-proxy` (README +
`docs.iron.sh`). No code was copied. An earlier revision wrongly
recorded the data plane as closed and unread; corrected 2026-08-11.
