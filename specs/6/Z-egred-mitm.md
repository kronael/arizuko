---
status: draft
depends: [Y-secret-broker]
---

# egred HTTPS-MITM — placeholder substitution at egress

Make `egred` (the forward proxy half of `crackbox`) optionally terminate
TLS for registered sources, swap `$VAR`-style placeholder tokens in
`Authorization` headers for real credentials drawn from arizuko's
existing folder/user secrets table, and re-encrypt to upstream. Models
itself on iron-proxy but reimplements only the small surface arizuko
actually needs; iron-proxy is studied, not vendored.

## Why this is not a replacement for the broker

Spec 6/Y dropped TLS-MITM in favor of the tool-broker:

> Per-user API tokens (GitHub, Jira, OpenAI, …) must reach external APIs
> on the user's behalf without materializing inside the agent container.
> The previous design (TLS-MITM on `egred` with placeholder substitution
> at egress) added a CA-distribution surface, an HTTP/1.1 ALPN constraint,
> and bytes-in-the-middle injection. Dropped: the agent is not the
> credential carrier.
> — `specs/6/Y-secret-broker.md` "Problem"

This spec ships MITM **anyway**, as the _second-best_ fallback covering
clients that don't go through MCP. The broker is correct for tool-mediated
secrets. It is **not** correct for the case the agent runs `curl`,
`python -m requests`, `node fetch(...)`, a vendor SDK, or any
operator-granted `bash` script that hits an external HTTP API and reads
its credential from env. Those clients see only the placeholder value
the container's env was seeded with (`ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY`)
and would 401 without an interception point.

Two ways to read this spec:

- **Additive**: MCP-mediated calls use the broker (no MITM cost). Opaque
  HTTP clients are caught by egred MITM. Same `secrets` table feeds both.
- **Strictly opt-in**: a source registered without `mitm: true` keeps
  today's SNI-passthrough behavior. Nothing changes for instances that
  don't enable it.

If a future spec proves all opaque clients can be wrapped (SDK
instrumentation, sandbox-side shim), this spec becomes unnecessary and
gets deprecated. Until then, the safety net earns its keep.

## Anatomy of iron-proxy — what to lift, adapt, drop

iron-proxy is ~50 KLOC. We need a small fraction. Per-subsystem call:

| iron-proxy subsystem                                                              | Verdict   | Notes for egred                                                                                                                                                                                                                                                                                                                                                                |
| --------------------------------------------------------------------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `internal/cagen/cagen.go` (137 LOC) — CA gen, RSA4096/Ed25519, PKCS8 PEM          | **lift**  | Port nearly verbatim to `crackbox/pkg/mitm/cagen.go`. Drop the RSA4096 default to Ed25519 (smaller, faster, modern clients are fine).                                                                                                                                                                                                                                          |
| `internal/certcache/cache.go` (174 LOC) — LRU per-SNI leaf, P256 keys, signs leaf | **lift**  | Port as `crackbox/pkg/mitm/leafcache.go`. Keep `hashicorp/golang-lru/v2/expirable` (tiny dep already in tree-friendly module).                                                                                                                                                                                                                                                 |
| `internal/proxy/proxy.go:serveHTTPSMITM` (`/tmp/ip-proxy.go:159-168`)             | **adapt** | egred's existing CONNECT handler grows a branch: if registered source has `mitm: true`, hand the hijacked conn to a `tls.Server` using `GetCertificate: leafcache.GetOrCreate` instead of `io.Copy`-splicing to the upstream. No second listener — MITM is a per-source decision at CONNECT-accept time, not a separate `:443`.                                                |
| `internal/proxy/proxy.go:getCertificate` (`/tmp/ip-proxy.go:222-229`)             | **lift**  | SNI → leaf, one-line wrapper.                                                                                                                                                                                                                                                                                                                                                  |
| `internal/transform/secrets/secrets.go` (593 LOC, pipeline shape)                 | **adapt** | We need a tiny fraction — placeholder→real swap on a small set of header names. Port as `crackbox/pkg/mitm/headers.go` (~80 LOC). Drop YAML config, drop body/path/query swap, drop inject mode, drop formatter templates, drop multi-header matchers. The single rule is "scan `Authorization` and `X-Api-Key`-class headers, substring-replace `$VAR` with `resolver(VAR)`". |
| `internal/transform/secrets/resolver.go` (392 LOC) + `op_resolver.go` + `aws_sm`  | **drop**  | Replaced wholesale by `crackbox/pkg/mitm/secrets.go` calling `store.LookupSecret`. No 1Password, no AWS Secrets Manager, no AWS SSM, no GCP Auth — arizuko has its own table.                                                                                                                                                                                                  |
| `internal/controlplane/poller.go` (config-reload watcher)                         | **drop**  | egred admin API already handles dynamic registration; `mitm` flag rides the same `POST /v1/register` path. Restart-to-reload for global config (CA path) is acceptable; arizuko restarts are cheap.                                                                                                                                                                            |
| gRPC management API (`proto/`, `internal/controlplane`)                           | **drop**  | egred has `:3129` admin API. Don't grow a second control plane.                                                                                                                                                                                                                                                                                                                |
| `internal/hostmatch` (per-host policy rules)                                      | **drop**  | egred's per-source-IP allowlist already gates _which_ hosts the source may reach; MITM has nothing to add about _who_ can call _where_.                                                                                                                                                                                                                                        |
| `internal/dnsguard`                                                               | **drop**  | egred can grow DNS guard separately (spec 9/15); orthogonal.                                                                                                                                                                                                                                                                                                                   |
| `internal/mcp` (MCP policy interceptor)                                           | **drop**  | arizuko owns the MCP surface upstream of egred; redundant here.                                                                                                                                                                                                                                                                                                                |
| `iron-proxy.example.yaml` (644 LOC of options)                                    | **drop**  | Configuration is per-source via the admin API, not via YAML. The TOML/env at `~/.crackboxrc` only carries the global CA path + leaf TTL.                                                                                                                                                                                                                                       |
| `internal/transform/pipeline.go` (multi-transform chain, audit emit)              | **drop**  | One transform (secrets), called inline. No registry, no pluggable transform list, no audit pipeline — egred logs to `secret_use_log` directly.                                                                                                                                                                                                                                 |

Roughly: **~300 LOC lifted, ~150 LOC adapted, the rest discarded.**

## egred-side architecture

```
crackbox/pkg/mitm/                       new package
  cagen.go        port of iron-proxy/internal/cagen/cagen.go
  leafcache.go    port of iron-proxy/internal/certcache/cache.go
  listener.go     hand off a hijacked CONNECT conn to tls.Server (the new bit)
  headers.go      placeholder-swap on Authorization-class headers
  secrets.go      resolver: scope=(folder|user) lookup via store.LookupSecret

crackbox/pkg/admin/registry.go            entry grows MITM bool + Placeholders map
crackbox/pkg/admin/api.go                 WireEntry grows the same two fields
crackbox/pkg/proxy/proxy.go               handleConnect branches on Entry.MITM
crackbox/cmd/crackbox/main.go             new `ca init` subcommand wraps cagen
```

Per-source registration shape (extends today's `WireEntry`):

```go
type WireEntry struct {
    IP           string            `json:"ip"`
    ID           string            `json:"id"`
    Allowlist    []string          `json:"allowlist"`
    MITM         bool              `json:"mitm,omitempty"`
    Placeholders map[string]string `json:"placeholders,omitempty"`
}
```

`Placeholders` maps the placeholder _literal_ (`"$ANTHROPIC_API_KEY"`)
to a **secret_ref** (`"user:alice-sub:ANTHROPIC_API_KEY"` or
`"folder:corp/eng:ANTHROPIC_API_KEY"`). The resolver parses the ref
and calls `store.LookupSecret(scope, scopeID, key)` at request time.
No values land in the registry on disk — only references do.

CONNECT flow with MITM on (rough):

```
1. Client (in container 10.99.0.42) sends CONNECT api.anthropic.com:443
2. egred handleConnect: src=10.99.0.42 → registry.Lookup → entry.MITM=true
3. egred returns "HTTP/1.1 200 Connection Established\r\n\r\n" to client
4. egred wraps the hijacked conn in tls.Server{ GetCertificate: leafcache.GetOrCreate }
5. Client TLS-handshakes against egred (sees leaf signed by arizuko-ca)
6. egred reads HTTP/1.1 request off the decrypted conn
7. headers.SwapPlaceholders(req, entry.Placeholders, resolver)
8. egred dials upstream (real TLS to api.anthropic.com:443), forwards request
9. Response streamed back through the same decrypted/re-encrypted seam
```

CA cert + key live at `/srv/data/store/crackbox-ca/ca.crt` (mode 0644)
and `/srv/data/store/crackbox-ca/ca.key` (mode 0600, owned by the egred
uid). Path is configurable via `CRACKBOX_CA_DIR`. Single CA per arizuko
instance — not shared across instances (see Decisions).

Leaf cert cache: SNI-hostname → leaf, LRU bound 1000 entries, expiry
720 h (30 d) — same as iron-proxy's recommended default
(`/tmp/ip-example.yaml:68`). Misses regenerate on demand. Process restart
loses the cache; first request per host pays one ECDSA-P256 sign
(~1 ms on a modern x86).

## CA distribution

Each HTTPS client library carries its own trust store. The CA cert must
be installed in every place a client looks. arizuko's krons run already
failed this once — Python `requests` ignored `update-ca-certificates`
because it bundles its own `certifi` store. The full list:

| Client                     | Trust mechanism                               | What to set                                                                                         |
| -------------------------- | --------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Go `net/http`              | System trust (`/etc/ssl/certs`)               | `update-ca-certificates` after copying to `/usr/local/share/ca-certificates/`                       |
| `curl`, `wget`             | System trust                                  | Same.                                                                                               |
| Node.js (`fetch`, `https`) | Bundled, plus `NODE_EXTRA_CA_CERTS`           | `ENV NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/arizuko-ca.crt`                           |
| Python `requests`          | `certifi` bundle                              | `ENV REQUESTS_CA_BUNDLE=/usr/local/share/ca-certificates/arizuko-ca.crt`                            |
| Python `urllib`, `httpx`   | OpenSSL default + `SSL_CERT_FILE`             | `ENV SSL_CERT_FILE=/usr/local/share/ca-certificates/arizuko-ca.crt`                                 |
| Ruby `Net::HTTP`           | OpenSSL default + `SSL_CERT_FILE`             | Same as above.                                                                                      |
| Rust `reqwest` (`rustls`)  | Bundled by `webpki-roots` — does NOT read env | Use `reqwest::Certificate::from_pem` in the SDK, or build with `native-tls` feature. **Hard case.** |
| Go programs pinning CAs    | Hard-coded `tls.Config.RootCAs`               | Cannot be intercepted. See Honest gaps.                                                             |

Concrete setup steps:

1. **Once per instance**, on host: `crackbox ca init`. Writes
   `/srv/data/store/crackbox-ca/{ca.crt,ca.key}`.
2. **In `arizuko-ant` Dockerfile**, add:
   ```dockerfile
   COPY arizuko-ca.crt /usr/local/share/ca-certificates/arizuko-ca.crt
   RUN update-ca-certificates
   ENV NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/arizuko-ca.crt \
       REQUESTS_CA_BUNDLE=/usr/local/share/ca-certificates/arizuko-ca.crt \
       SSL_CERT_FILE=/usr/local/share/ca-certificates/arizuko-ca.crt
   ```
3. **At container spawn**, `container/runner.go` mounts the per-instance
   CA cert (read-only) into `/usr/local/share/ca-certificates/arizuko-ca.crt`.
   The cert is also baked into the image; the mount is the source of truth
   when CAs are rotated without rebuilding the image.
4. **For the proxy itself**, the container env already sets
   `HTTPS_PROXY=http://egred:3128`. No change.

The Rust-rustls case (and any other bundled-roots client) cannot be
fixed by env. The fallback is documented under Honest gaps.

## Secret source binding

At container spawn, `gated`'s container runner builds the placeholder
map for that container's source IP and POSTs it to egred's
`/v1/register` along with the existing allowlist. Two pieces:

1. **What placeholders look like**. Identical syntax to folder env
   secrets: `$VAR_NAME`. No new mental model — what an agent sees as
   `ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY` in `env` is the same
   `$ANTHROPIC_API_KEY` the proxy swaps. Decision (c) below.
2. **How they resolve**. Each placeholder maps to a `secret_ref`
   (`folder:<path>:<key>` or `user:<sub>:<key>`). The runner picks the
   ref based on the spawn's owner: per-user spawns get `user:` refs;
   shared-folder spawns get `folder:` refs. Mixed (some keys user, some
   folder) is supported — the map is per-placeholder.

At request time, the MITM listener calls into a resolver:

```go
// crackbox/pkg/mitm/secrets.go
type Resolver interface {
    Resolve(ref string) (value string, ok bool)
}

// arizuko-side impl in gated wires:
// ref="user:alice-sub:GITHUB_TOKEN" → store.LookupSecret(user, "alice-sub", "GITHUB_TOKEN")
```

The resolver is an interface so egred ships without an arizuko
dependency. arizuko's `gated` registers an HTTP-backed resolver against
the admin API (`POST /v1/resolve`) — egred holds no DB handle. The
plaintext secret crosses the loopback once per request, never written
to disk on the egred side, never cached past the request lifetime.

Each successful swap emits one row to `secret_use_log` (same table 6/Y
writes to — Decision d):

```
secret_use_log(ts, spawn_id, caller_sub, folder, tool='egred:mitm',
               key, scope, status, latency_ms)
```

`tool='egred:mitm'` distinguishes MITM swaps from broker-mediated
tool calls in the same table — one grep splits them.

## Threat model

egred's host process today holds: per-source allowlists, no secrets.
With MITM on it holds: the CA private key (in process memory after
boot), and the plaintext secret for each in-flight request. Both are
higher-value than today.

| Asset                     | Existing scope                               | New mitigation                                                                                                                                                                                                                                                         |
| ------------------------- | -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| CA private key            | File at `/srv/data/store/crackbox-ca/ca.key` | Mode `0600`, owned by egred uid, never read by other processes. Loaded into memory at boot. Never logged. Rotation: `crackbox ca init --force` writes a new pair, restart egred, redeploy `arizuko-ant` image with the new cert.                                       |
| Plaintext secrets         | None today                                   | In-memory only for request lifetime. No request-level cache. Resolver call is per-request. Never logged. `secret_use_log` records the _fact_ of the swap, not the value.                                                                                               |
| Leaf certs                | None today                                   | In-memory LRU. ECDSA-P256 leaf keys never written to disk. Lost on restart (cache miss → regenerate).                                                                                                                                                                  |
| Compromised egred process | Process can pivot, no creds                  | Process can mint leaf certs for any SNI (impersonate arbitrary HTTPS sites _to containers that trust this CA_) and read every in-flight secret. Containment: egred runs as a low-privilege uid, on its own Docker network, with the CA key as its only on-disk secret. |

A compromised egred process is roughly as bad as a compromised gated
process — both can read the secrets table. MITM does not add a _new_
attack vector to the cluster's overall posture; it shifts the surface.

## Honest gaps

- **Pinned-cert clients**. Some SDKs (notably Rust `rustls` with
  bundled `webpki-roots`) ignore system trust. arizuko-ant doesn't ship
  any such SDK today; if the agent shells out to a binary that pins,
  MITM 401s. The escape hatch: `X-Crackbox-No-MITM: 1` header on the
  client request bypasses substitution (the proxy still splices to
  upstream — debug only). Decision g.
- **HTTP/2 + ALPN**. iron-proxy's `tls.Config.NextProtos` is HTTP/1.1
  only. We match that for v1: ALPN advertises `http/1.1`, so the
  client negotiates h1 and we parse with `net/http`. h2 to the upstream
  is unaffected (egred re-dials and can negotiate whatever upstream
  prefers). Out of scope for v1: h2 between client and proxy.
- **CONNECT vs HTTPS_PROXY**. Most clients honor `HTTPS_PROXY` and send
  CONNECT — fine. Some bypass env (Docker daemon, raw `tls.Dial`) —
  they hit upstream directly and the proxy never sees them. For agent
  containers we control the env at spawn; this is a low risk inside,
  high risk for SDKs that pin their HTTP transport.
- **Large request bodies**. egred today streams CONNECT splices without
  reading bodies. MITM mode buffers headers + the first chunk of body
  to do swaps (default cap 10 MB, matching iron-proxy
  `/tmp/ip-example.yaml:28`). Larger uploads stream past the swap point
  with no further inspection.
- **WebSocket / HTTP upgrade**. WS over HTTPS gets caught by MITM, the
  upgrade handshake completes, then the proxy hijacks bidirectionally
  — same as iron-proxy. No swap on WS frames; tokens in `Sec-WebSocket-Protocol`
  are out of scope.
- **Time-to-leak**. From client TLS handshake to upstream dial, the
  plaintext secret lives in process memory for ~milliseconds. A core
  dump during that window leaks. Run egred without dump-permitting
  ulimits and consider `prctl(PR_SET_DUMPABLE, 0)`.
- **CA rotation breaks in-flight containers**. Rotating the CA forces a
  redeploy of `arizuko-ant` (new cert in image trust store). v1 has no
  hot-rotation; document the 30 d leaf expiry and the ~yearly CA
  rotation cadence in `SECURITY.md`.

## Out of scope (v1)

- gRPC streaming MITM (HTTP/2-only protocol, h1 ALPN excludes it).
- Mutual TLS (client cert auth at egress).
- Response transforms (we only edit the outbound request).
- HTTP/2 between client and proxy.
- Transparent-mode MITM: the transparent listener stays SNI-passthrough.
  Forward-proxy mode is the only MITM path. Transparent + MITM together
  needs DNS interception so the client believes the upstream IP is
  egred's, and that's its own spec (9/15 territory).
- Per-host MITM disable (today: per-source). If a registered source
  has `mitm: true` and the allowlist permits the host, the host gets
  intercepted. Carve-outs use `X-Crackbox-No-MITM: 1`.
- Multi-tenant CA hierarchy. One CA per arizuko instance; no
  intermediate-CA-per-folder shaving.

## Effort estimate

| Bucket                                                                    | LOC         |
| ------------------------------------------------------------------------- | ----------- |
| `crackbox/pkg/mitm/cagen.go` (port of iron-proxy/internal/cagen)          | ~140        |
| `crackbox/pkg/mitm/leafcache.go` (port of iron-proxy/internal/certcache)  | ~170        |
| `crackbox/pkg/mitm/listener.go` (hijacked-CONNECT → tls.Server, new)      | ~120        |
| `crackbox/pkg/mitm/headers.go` (placeholder swap, adapt from secrets pkg) | ~80         |
| `crackbox/pkg/mitm/secrets.go` (resolver interface + HTTP impl)           | ~60         |
| `crackbox/pkg/admin/api.go` + `registry.go` deltas (MITM + Placeholders)  | ~30         |
| `crackbox/pkg/proxy/proxy.go` deltas (CONNECT branch)                     | ~40         |
| `crackbox/cmd/crackbox/main.go` (`ca init` subcommand)                    | ~50         |
| Tests (CA gen, leaf mint, header swap, end-to-end with real TLS)          | ~250        |
| `ant/Dockerfile` (CA install + env vars)                                  | ~5          |
| `container/runner.go` (mount CA + populate placeholder map on spawn)      | ~40         |
| arizuko-side resolver HTTP handler in `gated`                             | ~60         |
| `store/migrations/0065-egred-mitm.sql` (extend allowlist persistence?)    | ~10 (see ↓) |
| **Total net new in Go**                                                   | **~795**    |

The migration is small: per-source `Placeholders` map lives in the
admin registry's existing JSON state file (`CRACKBOX_STATE_PATH`). No
new SQL table needed. `secret_use_log` (0048) already exists from 6/Y.

Tests: leaf-cert chain verification with `x509.Verify`, header swap on
canonical/non-canonical casing (port iron-proxy's `replaceInHeader`
behavior), end-to-end with a fake upstream that asserts the swap
happened. Target ~250 LOC of test code; matches iron-proxy's ratio.

## Decisions

a. **MITM is opt-in per source registration, off by default.** Today's
`crackbox run` and `arizuko create` workflows keep SNI-passthrough.
Opting in is one bool on the register call. Reverts trivially.

b. **One CA per arizuko instance, not shared across instances.** Each
instance gets its own `crackbox-ca/`. Cross-instance secret leakage
via shared CA is impossible. Trade: every instance must distribute
its own CA to its containers (the image is per-instance anyway).

c. **Placeholders use `$VAR` syntax, same as folder env secrets.** No
new mental model. Operator who writes `ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY`
in a folder's env file gets the swap for free — same literal string,
one resolution path.

d. **Reuse `secret_use_log`, no new audit table.** Spec 6/Y already
built this. `tool='egred:mitm'` discriminates MITM swaps from broker
swaps. Same operator analytics work.

e. **HTTP/1.1 only between client and proxy for MITM in v1.** ALPN
advertises `http/1.1`. Upstream side is whatever upstream prefers.
h2-to-proxy is a v2 problem.

f. **We do NOT vendor iron-proxy.** We port the small surface we need
(~310 LOC across cagen + leafcache + the MITM-listener entrypoint)
and own its lifecycle. Their YAML, gRPC management API, 1Password
resolver, host-match rules, and transform pipeline all stay in their
repo. Boring code: we won't carry a dep we use 6% of.

g. **Per-request opt-out via `X-Crackbox-No-MITM: 1`.** The proxy still
handles CONNECT and splices, just skips the TLS-terminate step for
that request. Strictly for debugging; not exposed as agent UI.

## Cross-references

- [`specs/6/Y-secret-broker.md`](Y-secret-broker.md) — the preferred
  path for tool-mediated secrets. This spec is the safety net for what
  Y doesn't cover.
- [`specs/9/10-crackbox-arizuko.md`](../9/10-crackbox-arizuko.md) —
  egred's existing per-source allowlist surface, which this spec
  extends.
- [`specs/9/9-crackbox-standalone.md`](../9/9-crackbox-standalone.md) —
  crackbox's standalone shape (admin API at `:3129`); MITM rides the
  same surface.
