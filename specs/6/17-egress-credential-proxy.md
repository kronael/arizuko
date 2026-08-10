---
status: draft
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
depends: [8-crackbox-standalone, ../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/17 — egress credential proxy (the opaque-client hole)

Proposal to fill the one credential hole arizuko still has: an
**opaque HTTP client running inside the container** — the `aws` CLI,
`gh`, `boto3`, or any script the agent writes that calls an API
directly, not through an arizuko-brokered tool. `5/13`/`5/14` closed
every BROKERED path (connectors, REST descriptors), but a raw client
has no broker to hook. `5/13` §"Out of scope" names this as `8/Z`,
"MITM-isolated egress for opaque HTTP clients, additive" — that spec was
never written and `8/Z` does not exist. This is it, recast from what
Centaur actually ships.

## The hole, precisely

- **arizuko brokers only tools it dispatches.** A capability credential
  reaches a connector subprocess or a REST request ON THE HOST, per call,
  and is scrubbed from the result (`ipc/connector.go:120-127`,
  `ipc/extcall.go:59-97`). The container env never carries it (`5/14` §2,
  BUGS `X1` closed 2026-08-09).
- **A raw in-container client is unreachable by that broker.** Its bytes
  ride TLS the platform cannot see: crackbox CONNECT-tunnels HTTPS by
  splicing raw bytes (`crackbox/pkg/proxy/proxy.go:95,159`) after peeking
  only the SNI/ClientHello (`crackbox/pkg/proxy/peek.go:31,45`). It
  cannot inject or inspect anything inside the tunnel — response scanning
  is explicitly out of scope (`specs/6/8-crackbox-standalone.md:138`).
- **So the only way to feed a raw client a capability credential is to
  put it in container env — which `X1` forbids.** That collision is a
  live, filed casualty: BUGS `X2`, the aws-devops product, whose whole
  pitch is per-engineer `AWS_*` keys reaching `boto3` inside the
  container for CloudTrail attribution. `X1` removed the mechanism; the
  product now fails silently. `X2` option (a) is "a host-side SigV4
  connector" — which is exactly this spec's `aws_auth` transform.

## What Centaur does — and the sharp caveat

Centaur's answer is a **MITM egress proxy that swaps a placeholder for
the real credential host-side**, keyed by destination host. The
load-bearing idea, verified in their open control plane:

- **The tool holds a placeholder; the proxy holds the value.** A leaked
  placeholder is worthless because substitution is bound to a host. In
  their `inject` mode the client never sees a value at all — the proxy
  adds the header (`tools/research/websearch/pyproject.toml:36-39`).
- **Typed transforms per credential kind**, declared per tool and
  compiled into per-sandbox proxy config
  (`services/console/app/models/principal_sync_config_snapshot.rb:341-352`):
  `secrets` (replace/inject a header), `hmac_sign`, `aws_auth`,
  `oauth_token`, `gcp_auth`.
- **AWS SigV4 re-signing is the standout.** boto3 signs each request with
  throwaway placeholder keys; the proxy strips and re-signs with the real
  read-only IAM keys, scoped by `allowed_services`
  (`tools/infra/cloudwatch/pyproject.toml:29-47`). The real keys never
  enter the tool process. This is `X2`'s attribution requirement, solved
  without the credential ever touching the container.
- **The sandbox's own identity JWT is injected BY the proxy**, never held
  in the sandbox
  (`services/console/app/models/principal_sync_config_snapshot.rb:236-240`)
  — the cleanest expression of the whole model.

**The caveat that reshapes the proposal — three findings that changed the
plan mid-research:**

1. **Their MITM data plane is a closed third-party binary.** `services/iron-proxy/`
   is a 14-line Dockerfile over a pinned image
   (`services/iron-proxy/Dockerfile:3`,
   `FROM ironsh/iron-proxy:0.49.0@sha256:c462…`); the repo has **zero Go
   files**. Every TLS/ALPN/HPACK/body-buffering mechanic lives in code
   we cannot read, and **no test in their repo ever starts the proxy**
   (`centaur-sandbox-e2e/tests/support/mod.rs:387` points the control
   client at a dead `http://127.0.0.1:1`). What is open is the CONTROL
   plane (a Rails app, "iron-control") + config translators — the design,
   not the engine. So there is nothing to import; only a design to copy
   and build in crackbox.
2. **Response-body secret scanning — the thing the brief asked me to
   study — does not exist in Centaur.** Grepping their whole tree for
   response/scan/dlp/redact/gzip found no response-side transform, no
   leak detection, no size limit. Credentials flow outbound under policy;
   what comes back is unexamined. (crackbox already declined response
   scanning too, `6/8:138`.) If arizuko wants it, Centaur is not the
   reference — it is greenfield, and it belongs in a separate spec.
3. **Centaur's match-failure default is fail-OPEN, and they hardcode it
   that way.** When a host matches but the credential doesn't resolve,
   the secret is silently dropped and the PLACEHOLDER goes upstream as a
   literal (`services/console/docs/API.md:145`;
   observed as real 401s, `centaur-sandbox-agent-k8s/src/iron_proxy.rs:61-73`).
   Their one fail-closed lever, `require`, is hardcoded `false` on the
   tool path (`centaur-perms/src/translate.rs:188,191`), and its
   semantics live in the closed binary, documented nowhere. **arizuko
   must invert this**: a declared host with an unresolved credential
   BLOCKS the request (`5/14`/`5/13` fail-loud discipline, CLAUDE.md
   §"Fail loud"). A placeholder reaching an upstream is a credential-shaped
   token in someone's logs.

## The delta: crackbox gains an opt-in MITM inject mode

crackbox already terminates the client's CONNECT and dials upstream. The
addition is a per-host decision at that seam: **splice (today) OR
terminate-inspect-inject (new)**, chosen by whether a transform rule
matches the peeked SNI host. The pieces:

- **A CA, generated once per instance, cert distributed to the container,
  key held only by crackbox.** Centaur's split is correct
  (`bootstrap-k8s-secrets.sh:384-396`, sandbox mounts cert-only) — copy
  the split, NOT their unrotated 10-year shell-script CA. Container trust
  is env-var: `NODE_EXTRA_CA_CERTS`/`REQUESTS_CA_BUNDLE`/`SSL_CERT_FILE`/
  `CURL_CA_BUNDLE`/`GIT_SSL_CAINFO` at spawn (Centaur's set,
  `iron_proxy.rs:1687-1703`) — with the same known gap they hit and
  worked around three times: rustls/Deno/JVM clients ignore these vars
  (`tools/productivity/gsuite/client.py:30-41`,
  `tools/ruff.toml:16`). Document it; do not pretend it is total.
- **Transform rules driven by the SAME broker credential store** —
  `store.FolderSecretsResolvedForUser` (`store/secrets.go:449`), the
  reader the connector broker already uses. One credential store, two
  injection sites (brokered tool = `5/13`; opaque client = this proxy).
  A rule is `(host-glob, transform)`; the transform vocabulary is
  `6/19`'s typed secret entries (`http` replace/inject, `hmac_sign`,
  `aws_auth`). crackbox's `match.Host` (`crackbox/pkg/match/match.go:46`)
  already does host-glob matching — reuse it, do not re-implement it in a
  second place (Centaur's cautionary tale: they re-implemented host
  matching in Ruby and admit it is only "close enough",
  `principal_sync_config_snapshot.rb:354-370`).
- **Fail-closed on match without resolve** (the inversion above).
- **HTTP/1.1 only, stated.** Centaur's pipeline is HTTP/1.1-shaped (their
  header allowlist enumerates HTTP/1.1 hop-by-hop headers,
  `services/iron-proxy/iron-proxy.yaml:55-62`; no ALPN control anywhere)
  and h2/HPACK/gRPC-trailers are unmentioned in their entire tree. A
  Go MITM proxy that forces `http/1.1` in ALPN and rejects what it can't
  parse is the honest v1. Say so; don't claim h2.

## Cost

Real, and concentrated in crackbox (a shipped standalone component,
`6/8`): a TLS-termination path beside the splice path, per-host leaf
minting + cache, CA lifecycle (generate at `arizuko create`, mount at
spawn, rotate — a real operational surface arizuko does not have today),
the transform executors (`http` inject is trivial; `aws_auth` SigV4
re-signing is the one with real crypto), and a config feed from the
broker store to crackbox (crackbox's admin API already takes per-source
registration, `6/8:37` — extend the registration payload with rules).
Operationally it adds a decrypting middlebox to the egress path: a bug
there is a wrong-credential-to-wrong-host bug, so it needs the fail-closed
posture and tests that assert a placeholder NEVER reaches an upstream.

## Why it is worth it

It closes the last credential gap and it un-breaks a shipped product. The
brokered-tool model (`5/13`) is arizuko's and is genuinely ahead of
Centaur's placeholder-swap for tools arizuko dispatches — the credential
never enters the container at all, versus Centaur's placeholder-in-env.
But arizuko has NO answer for the opaque client, and "just don't run
`aws` in the container" is not one when a product's whole value is per-user
cloud attribution (`X2`). This is the additive layer `5/13` already
anticipated by name.

## Recommendation

Sign this off FIRST among the Centaur specs, but build it **gated on
`X2`'s design decision** — it IS `X2` option (a) generalized. If the user
picks (c) retire aws-devops, this shrinks to "nice to have"; if (a), this
is the mechanism and should be built as scoped here. Do NOT build the CA
lifecycle speculatively — it earns its operational cost only with a
consumer, and `X2` is the consumer.

## Attribution

Design derived from reading `paradigmxyz/centaur` (Apache-2.0), commit
`20f3021`: `services/iron-proxy/`,
`services/api-rs/crates/centaur-iron-control/`, `centaur-iron-proxy/`,
`centaur-perms/`, `centaur-sandbox-agent-k8s/src/iron_proxy.rs`,
`services/console/` (iron-control), `tools/*/pyproject.toml`,
`contrib/scripts/bootstrap-k8s-secrets.sh`. Their MITM data plane
(`ironsh/iron-proxy`) is closed and was NOT read; nothing is derived from
it. No code was copied.
