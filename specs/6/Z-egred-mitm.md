---
status: draft
depends: [Y-secret-broker, 5/6-middleware-pipeline]
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

| iron-proxy subsystem                                                              | Verdict   | Notes for egred                                                                                                                                                                                                                                                                                                                                                                           |
| --------------------------------------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/cagen/cagen.go` (137 LOC) — CA gen, RSA4096/Ed25519, PKCS8 PEM          | **lift**  | Port nearly verbatim to `crackbox/pkg/mitm/cagen.go`. Drop the RSA4096 default to Ed25519 (smaller, faster, modern clients are fine).                                                                                                                                                                                                                                                     |
| `internal/certcache/cache.go` (174 LOC) — LRU per-SNI leaf, P256 keys, signs leaf | **lift**  | Port as `crackbox/pkg/mitm/leafcache.go`. Keep `hashicorp/golang-lru/v2/expirable` (tiny dep already in tree-friendly module).                                                                                                                                                                                                                                                            |
| `internal/proxy/proxy.go:serveHTTPSMITM` (`/tmp/ip-proxy.go:159-168`)             | **adapt** | egred's existing CONNECT handler grows a branch: if registered source has `mitm: true`, hand the hijacked conn to a `tls.Server` using `GetCertificate: leafcache.GetOrCreate` instead of `io.Copy`-splicing to the upstream. No second listener — MITM is a per-source decision at CONNECT-accept time, not a separate `:443`.                                                           |
| `internal/proxy/proxy.go:getCertificate` (`/tmp/ip-proxy.go:222-229`)             | **lift**  | SNI → leaf, one-line wrapper.                                                                                                                                                                                                                                                                                                                                                             |
| `internal/transform/secrets/secrets.go` (593 LOC, pipeline shape)                 | **adapt** | Replace with a ~5 LOC `injectSecretsBroker` shim that imports Y-secret-broker's `injectSecrets` middleware (see 6/Y `## Resolution (the broker middleware)`) and slots it into the chain keyed by the source's `secret_scope`. egred scans no headers, holds no placeholder map, and runs no parallel resolver. No YAML, no body/path/query swap, no inject mode, no formatter templates. |
| `internal/transform/secrets/resolver.go` (392 LOC) + `op_resolver.go` + `aws_sm`  | **drop**  | Resolution is the broker's job; egred owns no store lookup, no cache, no audit table. egred imports the broker's middleware function and lists it in the chain — same shape MCP dispatch uses (see 6/Y "the handler shape converges"). No 1Password, no AWS SM, no GCP — arizuko already has Y's resolver.                                                                                |
| `internal/controlplane/poller.go` (config-reload watcher)                         | **drop**  | egred admin API already handles dynamic registration; `mitm` flag rides the same `POST /v1/register` path. Restart-to-reload for global config (CA path) is acceptable; arizuko restarts are cheap.                                                                                                                                                                                       |
| gRPC management API (`proto/`, `internal/controlplane`)                           | **drop**  | egred has `:3129` admin API. Don't grow a second control plane.                                                                                                                                                                                                                                                                                                                           |
| `internal/hostmatch` (per-host policy rules)                                      | **drop**  | egred's per-source-IP allowlist already gates _which_ hosts the source may reach; MITM has nothing to add about _who_ can call _where_.                                                                                                                                                                                                                                                   |
| `internal/dnsguard`                                                               | **drop**  | egred can grow DNS guard separately (spec 9/15); orthogonal.                                                                                                                                                                                                                                                                                                                              |
| `internal/mcp` (MCP policy interceptor)                                           | **drop**  | arizuko owns the MCP surface upstream of egred; redundant here.                                                                                                                                                                                                                                                                                                                           |
| `iron-proxy.example.yaml` (644 LOC of options)                                    | **drop**  | Configuration is per-source via the admin API, not via YAML. The TOML/env at `~/.crackboxrc` only carries the global CA path + leaf TTL.                                                                                                                                                                                                                                                  |
| `internal/transform/pipeline.go` (multi-transform chain, audit emit)              | **drop**  | egred runs the same `Chain(slice, terminal)` idiom as `proxyd/main.go` (spec 5/6) — no separate transform registry. Audit is one middleware (`auditMITM`) writing to the broker's `secret_use_log` with `caller='egred'`.                                                                                                                                                                 |

Roughly: **~300 LOC lifted, ~150 LOC adapted, the rest discarded.**

## egred-side architecture

```
crackbox/pkg/mitm/                       new package
  cagen.go        port of iron-proxy/internal/cagen/cagen.go
  leafcache.go    port of iron-proxy/internal/certcache/cache.go
  chain.go        []func(http.Handler) http.Handler + Chain(slice, terminal) reducer
  bypass.go       bypassCheck middleware: SNI allowlist → passthrough decision
  tls.go          tlsTerminate middleware: hijack CONNECT, hand to tls.Server, leaf cert
  inject.go       injectSecretsBroker shim importing Y's injectSecrets
  audit.go        auditMITM middleware: emit secret_use_log row, caller='egred'
  bodycap.go      bodyCap middleware: 10 MB request-body cap

crackbox/pkg/admin/registry.go            entry grows MITM bool + BypassMITM + SecretScope
crackbox/pkg/admin/api.go                 WireEntry grows the same three fields
crackbox/pkg/proxy/proxy.go               handleConnect runs the chain
crackbox/cmd/crackbox/main.go             new `ca init` subcommand wraps cagen
```

The data path is a `[]func(http.Handler) http.Handler` slice reduced
into a single handler by `Chain(slice, terminal)` — same idiom as
`proxyd/main.go` (see `specs/5/6-middleware-pipeline.md ## 6/6c HTTP
chain`):

```go
var mitmChain = []func(http.Handler) http.Handler{
    bypassCheck,          // SNI in entry.BypassMITM → io.Copy splice, skip rest
    tlsTerminate,         // hijack CONNECT, tls.Server with leaf from leafcache
    injectSecretsBroker,  // imports Y-secret-broker's injectSecrets, keyed by entry.SecretScope
    auditMITM,            // one secret_use_log row per request, tool='egred:mitm'
    bodyCap,              // first 10 MB buffered for header swap; rest streams past
}
handler := Chain(mitmChain, forwardUpstream)
```

`forwardUpstream` is the terminal: real TLS dial to the SNI host,
re-encrypt the (possibly swapped) request, stream the response back.

Per-source registration shape (extends today's `WireEntry`):

```go
type WireEntry struct {
    IP          string   `json:"ip"`
    ID          string   `json:"id"`
    Allowlist   []string `json:"allowlist"`
    MITM        bool     `json:"mitm,omitempty"`
    BypassMITM  []string `json:"bypass_mitm,omitempty"`
    SecretScope string   `json:"secret_scope,omitempty"`
}
```

`SecretScope` names the folder whose secrets the broker is allowed to
see for this source (`"corp/eng"`, `"solo/inbox"`, …). egred never
holds the placeholder map and never resolves anything — it just passes
`SecretScope` to the broker middleware, which enforces visibility at
request time. `BypassMITM` holds per-source SNI rules evaluated by
`bypassCheck` before TLS — see `## Escape hatches` for the matcher and
operator guidance.

CONNECT flow with MITM on (rough):

```
1. Client (in container 10.99.0.42) sends CONNECT api.anthropic.com:443
2. egred handleConnect: src=10.99.0.42 → registry.Lookup → entry
3. bypassCheck: entry.MITM && no rule in entry.BypassMITM matches → continue chain
4. egred returns "HTTP/1.1 200 Connection Established\r\n\r\n" to client
5. tlsTerminate: wrap hijacked conn in tls.Server{ GetCertificate: leafcache.GetOrCreate }
6. Client TLS-handshakes against egred (sees leaf signed by arizuko-ca)
7. egred reads HTTP/1.1 request off the decrypted conn
8. injectSecretsBroker: call broker scoped to entry.SecretScope, swap Authorization-class headers
9. auditMITM: write secret_use_log row (caller='egred', tool='egred:mitm')
10. forwardUpstream: dial upstream (real TLS to api.anthropic.com:443), forward request
11. Response streamed back through the same decrypted/re-encrypted seam
```

If `bypassCheck` short-circuits (no `entry.MITM`, or a `bypass_mitm`
rule matches the CONNECT host), the chain stops at step 3 and egred
falls back to today's raw `io.Copy` splice. No leaf is minted, no
plaintext is observed, no audit row is written.

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

Each HTTPS client library carries its own trust store. arizuko's krons
run failed this once — Python `requests` ignored
`update-ca-certificates` because it bundles its own `certifi` store.
Coverage matrix:

| Client                                  | Trust mechanism                 | What to set                                                             |
| --------------------------------------- | ------------------------------- | ----------------------------------------------------------------------- |
| Go `net/http`, `curl`, OpenSSL, Ruby    | System trust                    | `COPY` + `update-ca-certificates` in `ant/Dockerfile`.                  |
| Rust `rustls-native-certs`              | Reads system trust              | Same.                                                                   |
| Python `requests`                       | `certifi` bundle                | `REQUESTS_CA_BUNDLE=/etc/ssl/certs/arizuko-ca.pem` (compose env).       |
| Python `urllib`, `httpx`                | OpenSSL + `SSL_CERT_FILE`       | `SSL_CERT_FILE=/etc/ssl/certs/arizuko-ca.pem` (compose env).            |
| Node.js (`fetch`, `https`)              | Bundled + `NODE_EXTRA_CA_CERTS` | `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/arizuko-ca.pem` (compose env).      |
| Rust `webpki-roots` (compiled-in roots) | Ignores system entirely         | Register host under per-source `bypass_mitm` — see `## Escape hatches`. |
| Go binaries with custom `tls.Config`    | Hard-coded `RootCAs`            | Same — `bypass_mitm`. No runtime patching.                              |

Setup:

1. **Once per instance**, on host: `crackbox ca init`. Writes
   `/srv/data/store/crackbox-ca/{cert.pem,key.pem}`.
2. **`ant/Dockerfile`** installs the CA into the system trust store at
   image build:
   ```dockerfile
   COPY arizuko-ca.crt /usr/local/share/ca-certificates/
   RUN update-ca-certificates
   ```
   That covers Go, curl, OpenSSL, Python with default bundle, and
   rustls-native-certs in one line.
3. **Compose env vars** (one knob per client family that ignores
   system trust):
   ```
   NODE_EXTRA_CA_CERTS=/etc/ssl/certs/arizuko-ca.pem
   REQUESTS_CA_BUNDLE=/etc/ssl/certs/arizuko-ca.pem
   SSL_CERT_FILE=/etc/ssl/certs/arizuko-ca.pem
   ```
4. **Compose bind-mount** the live CA from
   `/srv/data/store/crackbox-ca/cert.pem` into the container as
   `/usr/local/share/ca-certificates/arizuko-ca.crt` (read-only). The
   container entrypoint runs `update-ca-certificates` before launching
   ant so the mounted cert overrides the baked-in one. CA rotation
   (`crackbox ca init --force` + container restart) propagates without
   an image rebuild.
5. **For the proxy itself**, the container env already sets
   `HTTPS_PROXY=http://egred:3128`. No change.

Bundled-root rustls (`webpki-roots`) and certificate-pinning SDKs
ignore everything above; both are handled by `bypass_mitm` (see
`## Escape hatches`). No helper binary needed — the per-source bypass
list is already the answer for clients we cannot reach through the
trust store.

## Secret source binding

egred binds no secrets. The MITM path is a broker caller: at container
spawn, `gated`'s container runner POSTs the source's `WireEntry` to
egred's `/v1/register` carrying `secret_scope` (the folder whose
secrets this source may see). egred persists the scope and nothing
else — no placeholder map, no `secret_ref` strings, no plaintext.

At request time, `injectSecretsBroker` calls the broker's
`injectSecrets` middleware (spec 6/Y `## Resolution (the broker
middleware)`) with `entry.SecretScope` as the visibility key. The
broker holds the resolved value for request lifetime, in `gated`'s
process, and returns swapped headers. egred re-encrypts to upstream
and forgets.

Audit is `auditMITM`, the next link in the chain — it writes one row
per request to `secret_use_log` (the same table 6/Y writes to):

```
secret_use_log(ts, spawn_id, caller_sub, folder, tool='egred:mitm',
               key, scope, status, latency_ms, caller='egred')
```

`caller='egred'` distinguishes MITM-path swaps from MCP-path swaps
written by the broker's own audit middleware; one grep splits them.
egred owns no store handle and no in-process secret cache. Removing
the parallel resolver is decision (c).

## Threat model

egred's host process today holds: per-source allowlists, no secrets.
With MITM on it adds the CA private key (in process memory after
boot). It does **not** add plaintext secrets — those live in the
broker (in `gated`'s process) for request lifetime, and the swap
happens inside `injectSecretsBroker` against headers egred forwards.
The attack surface on egred shrinks compared to a self-contained
resolver design.

| Asset                     | Existing scope                               | New mitigation                                                                                                                                                                                                                  |
| ------------------------- | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| CA private key            | File at `/srv/data/store/crackbox-ca/ca.key` | Mode `0600`, owned by egred uid, never read by other processes. Loaded into memory at boot. Never logged. Rotation: `crackbox ca init --force` writes a new pair, restart egred; new CA propagates to containers on next spawn. |
| Plaintext secrets         | None — broker holds them in `gated`          | egred never sees a plaintext secret on disk or in memory between requests. Swap happens in `injectSecretsBroker` (broker call), in-flight only. Compromised egred cannot dump the secrets table — it has no handle to it.       |
| Leaf certs                | None today                                   | In-memory LRU. ECDSA-P256 leaf keys never written to disk. Lost on restart (cache miss → regenerate).                                                                                                                           |
| Compromised egred process | Process can pivot, no creds                  | Process can mint leaf certs for any SNI (impersonate arbitrary HTTPS sites _to containers that trust this CA_) and observe in-flight swapped headers. Cannot enumerate the secrets table; cannot read scopes it doesn't proxy.  |

A compromised egred process can see secrets _it actively proxies_ but
cannot enumerate the rest of the secrets table — that takes a
compromised `gated`. MITM does not collapse egred and gated into one
trust boundary; it adds the CA key to egred and leaves secrets where
they were.

## Honest gaps

- **Pinned-cert clients**. Some SDKs (notably Rust `rustls` with
  bundled `webpki-roots`) ignore system trust. arizuko-ant doesn't ship
  any such SDK today; if the agent shells out to a binary that pins,
  MITM 401s. Handled by per-source `bypass_mitm` hostname rules — see
  `## Escape hatches`. The host is registered for SNI-passthrough and
  placeholder substitution does not fire for that destination.
- **HTTP/2 + ALPN**. iron-proxy's `tls.Config.NextProtos` is HTTP/1.1
  only. We match that for v1: ALPN advertises `http/1.1`. h2-required
  destinations are registered under `bypass_mitm` so the client
  negotiates h2 directly with upstream. h2-between-client-and-proxy
  MITM is a v2 problem.
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
- **Time-to-leak**. Plaintext secrets live in the broker (`gated`)
  for request lifetime. egred sees swapped headers between
  `injectSecretsBroker` and upstream dial — ~milliseconds. A core
  dump on either process during that window leaks. Run both without
  dump-permitting ulimits and consider `prctl(PR_SET_DUMPABLE, 0)`.
- **CA rotation breaks in-flight containers**. The live CA is
  bind-mounted from `/srv/data/store/crackbox-ca/cert.pem`; rotation
  (`crackbox ca init --force` + container restart) propagates without
  an image rebuild. In-flight containers continue trusting the old CA
  until respawn. v1 has no hot-rotation inside a live container;
  document the 30 d leaf expiry and the ~yearly CA rotation cadence
  in `SECURITY.md`.

## Escape hatches

The v1 escape hatch is decided at source registration time, not per
request. CONNECT has to choose passthrough or MITM before any TLS
bytes move, so arizuko adds `bypass_mitm` to the registered
`WireEntry` and removes the request-header design entirely.

`WireEntry` becomes:

```go
type WireEntry struct {
    IP          string   `json:"ip"`
    ID          string   `json:"id"`
    Allowlist   []string `json:"allowlist"`
    MITM        bool     `json:"mitm,omitempty"`
    BypassMITM  []string `json:"bypass_mitm,omitempty"`
    SecretScope string   `json:"secret_scope,omitempty"`
}
```

`BypassMITM` is a per-source SNI rule set. Entries are exact hosts
(`"api.openai.com"`) or a single-label wildcard prefix
(`"*.googleapis.com"`). Matching is case-insensitive on the normalized
hostname with no trailing dot. IP literals never match wildcard rules.

The CONNECT path decides before TLS. `crackbox/pkg/proxy/proxy.go`
reads the authority host from `CONNECT host:443`, looks up the source
entry, runs the chain — `bypassCheck` is the first middleware and
short-circuits to `io.Copy` splice when a `bypass_mitm` rule matches;
otherwise the rest of the chain runs (`tlsTerminate` →
`injectSecretsBroker` → `auditMITM` → `bodyCap` → `forwardUpstream`).

```go
func shouldMITM(entry WireEntry, connectHost string) bool {
    if !entry.MITM {
        return false
    }
    host := normalizeHost(connectHost)
    if host == "" {
        return false
    }
    for _, rule := range entry.BypassMITM {
        if sniMatch(rule, host) {
            return false
        }
    }
    return true
}

func sniMatch(rule, host string) bool {
    r := strings.ToLower(strings.TrimSuffix(rule, "."))
    h := strings.ToLower(strings.TrimSuffix(host, "."))
    if r == h {
        return true
    }
    if !strings.HasPrefix(r, "*.") {
        return false
    }
    suffix := strings.TrimPrefix(r, "*")
    if !strings.HasSuffix(h, suffix) {
        return false
    }
    left := strings.TrimSuffix(h, suffix)
    return left != "" && !strings.Contains(left[:len(left)-1], ".")
}
```

`normalizeHost` uses the CONNECT authority, not an HTTP header from
inside TLS. The handler does not wait for ClientHello to discover SNI.
If the client CONNECTs by IP address there is no hostname signal to
match, so the source either runs full passthrough (`mitm: false`) or
full MITM with likely certificate failure. v1 documents that
host-based bypass requires hostname CONNECT.

Registration is written by `container/runner.go` when the spawn is
created. The runner already knows the per-source allowlist and the
spawn's folder; it now also sends `secret_scope` and `bypass_mitm`
in the same `POST /v1/register` body to egred `:3129`. Operators
extend the bypass list through the same arizuko-side surface; no
in-band agent API is added.

**Rustls native-roots and other system-trust clients — image +
compose.** The CA lands in the system trust store at image build
(`COPY` + `update-ca-certificates` in `ant/Dockerfile`) and the live
per-instance CA is bind-mounted on top by compose so rotation does
not require an image rebuild. See `## CA distribution`. Fixes
`rustls-native-certs` and any client that reads the system store;
does NOT fix `webpki-roots` or embedded root sets — those use
`bypass_mitm`.

**Pinned-cert clients — `bypass_mitm`.** No fallback header, no
best-effort partial interception. Operator lists the pinned host
under `bypass_mitm`; egred splices raw TCP after the
`200 Connection Established` and placeholder substitution does not
run for that host. Starter defaults to suggest in docs and examples:
`api.stripe.com`, `upload.stripe.com`, `oauth2.googleapis.com`,
`*.googleapis.com`, `api.github.com`, `uploads.github.com`,
`api.cloudflare.com`, `*.cloudflare.com`, `s3.amazonaws.com`,
`*.s3.amazonaws.com`, `sts.amazonaws.com`, `*.amazonaws.com`.

**HTTP/2 — h1-only with `bypass_mitm` escape.**
`crackbox/pkg/mitm/tls.go` advertises
`NextProtos: []string{"http/1.1"}` and does not ship h2 MITM in v1.
H2-required hosts go in `bypass_mitm`; egred uses pure CONNECT
passthrough so the client negotiates h2 directly with upstream. There
is no ALPN-refusal hint protocol — the operator action is to add the
hostname.

**Go custom `tls.Config` — detect by hostname and bypass.** Go
binaries are statically linked; `LD_PRELOAD`, process injection, and
runtime patching are not viable. If a Go SDK builds its own
`tls.Config{RootCAs: ...}` and rejects the arizuko CA, the operator
adds the target host to `bypass_mitm`. v1 does not attempt automatic
detection; documented hostname lists and 401s in `secret_use_log`
(status='err') drive the operator update.

**Concrete code shape.**

- `ant/Dockerfile` — `COPY` + `update-ca-certificates` at image build.
- `compose/compose.go` — bind-mount the live CA from
  `/srv/data/store/crackbox-ca/cert.pem` and set the env-var fan
  (`NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE`).
- `crackbox/pkg/admin/api.go` + `registry.go` — persist `BypassMITM`
  and `SecretScope`.
- `crackbox/pkg/proxy/proxy.go` — runs the chain at CONNECT;
  `bypassCheck` is the first middleware (decides splice vs MITM).
- `crackbox/pkg/mitm/tls.go` — `NextProtos: []string{"http/1.1"}`.
- Operator-facing docs include the starter bypass host list and the
  failure-mode table from `## CA distribution`.

**Threat-model delta.** `bypass_mitm` widens the
direct-to-upstream surface for listed hosts. Those flows still transit
egred as a CONNECT splice, so source allowlist enforcement remains in
place — but secret substitution does not occur and egred never sees
plaintext HTTP for those destinations. That is the point: the secret
never leaves the agent through egred for bypassed hosts. Implication
is operational, not cryptographic — any workflow that relied on
placeholder swap for a bypassed host fails unless the client has a
real credential by some other path (broker, env, OAuth flow).

Safer than the dead per-request header: a bypassed host is decided
from source registration and CONNECT authority, both visible before
TLS. No attacker-controlled in-band signal can disable interception
after policy has been chosen.

**What we still cannot intercept:**

- Clients that CONNECT to a raw IP and validate against an IP SAN or
  custom verifier; host-pattern `bypass_mitm` has no signal to match.
- Clients with embedded roots or SPKI pinning for hosts not listed in
  `bypass_mitm`; they fail closed until the operator adds the host.
- Client-to-proxy HTTP/2, including gRPC over CONNECT, when the source
  is in MITM mode for that host.
- Processes that ignore `HTTPS_PROXY` and `tls.Dial` direct to the
  internet — they bypass egred entirely (host-side iptables in
  transparent mode is the answer, not MITM).
- mTLS egress where client cert handshake semantics must pass through
  untouched; v1 requires bypass for those hosts.

## Out of scope (v1)

- gRPC streaming MITM and general HTTP/2 MITM between client and proxy.
- Mutual TLS (client cert auth at egress).
- Response transforms (we only edit the outbound request).
- Transparent-mode MITM: the transparent listener stays SNI-passthrough.
  Forward-proxy mode is the only MITM path. Transparent + MITM together
  needs DNS interception so the client believes the upstream IP is
  egred's, and that's its own spec (9/15 territory).
- Automatic detection of pinned-cert, bundled-root rustls, or
  custom-`tls.Config` clients beyond operator-maintained hostname
  bypass lists.
- Per-request MITM disable signals of any kind. v1 only supports
  per-source `bypass_mitm` host rules decided at CONNECT time.
- Multi-tenant CA hierarchy. One CA per arizuko instance; no
  intermediate-CA-per-folder shaving.

## Effort estimate

Prior round (custom resolver + `arizuko-ca-sync` helper) estimated
**~895 LOC net new in Go**. Broker unification and CA simplification
remove four buckets:

- `crackbox/pkg/mitm/headers.go` (placeholder swap) — **~80 LOC gone**
- `crackbox/pkg/mitm/secrets.go` (resolver interface + HTTP impl) — **~60 LOC gone**
- arizuko-side resolver HTTP handler in `gated` — **~60 LOC gone**
- `arizuko-ant/cmd/arizuko-ca-sync/main.go` — **~80 LOC gone**

Net deletion: **~280 LOC**. New total drops to **~720 LOC net new**.

| Bucket                                                                                   | LOC      |
| ---------------------------------------------------------------------------------------- | -------- |
| `crackbox/pkg/mitm/cagen.go` (port of iron-proxy/internal/cagen)                         | ~140     |
| `crackbox/pkg/mitm/leafcache.go` (port of iron-proxy/internal/certcache)                 | ~170     |
| `crackbox/pkg/mitm/chain.go` (`Chain(slice, terminal)` reducer)                          | ~10      |
| `crackbox/pkg/mitm/bypass.go` (`bypassCheck` middleware + `sniMatch`)                    | ~60      |
| `crackbox/pkg/mitm/tls.go` (`tlsTerminate` middleware: hijacked-CONNECT → tls.Server)    | ~100     |
| `crackbox/pkg/mitm/inject.go` (`injectSecretsBroker` shim importing Y's `injectSecrets`) | ~5       |
| `crackbox/pkg/mitm/audit.go` (`auditMITM` middleware → `secret_use_log`)                 | ~30      |
| `crackbox/pkg/mitm/bodycap.go` (`bodyCap` middleware, 10 MB)                             | ~30      |
| `crackbox/pkg/admin/api.go` + `registry.go` deltas (MITM + BypassMITM + SecretScope)     | ~30      |
| `crackbox/pkg/proxy/proxy.go` deltas (run the chain at CONNECT)                          | ~50      |
| `crackbox/cmd/crackbox/main.go` (`ca init` subcommand)                                   | ~50      |
| `ant/Dockerfile` (CA `COPY` + `update-ca-certificates` + env vars)                       | ~5       |
| `compose/compose.go` (CA bind-mount + env-var fan)                                       | ~5       |
| `container/runner.go` (bypass + secret_scope on spawn)                                   | ~30      |
| Tests (CA gen, leaf mint, chain order, `sniMatch`, end-to-end with real TLS)             | ~245     |
| **Total**                                                                                | **~960** |

The table sums to ~960 because it counts new code in absolute terms;
~240 of that is tests, leaving ~720 of production code. Headline is
**~720 LOC net new** vs ~895 prior — the unification + CA
simplification pay for themselves in deleted future work.

Per-source registry state grows by one string (`secret_scope`) and
one slice (`bypass_mitm`); the existing JSON state file
(`CRACKBOX_STATE_PATH`) carries both. No new SQL table.
`secret_use_log` (0048) already exists from 6/Y; the schema gains a
`caller` column owned by 6/Y's migration.

Tests: leaf-cert chain verification with `x509.Verify`, middleware
order (bypass short-circuits before `tlsTerminate`), end-to-end with
a fake upstream that asserts `injectSecretsBroker` swapped the
`Authorization` header.

## Decisions

a. **MITM is opt-in per source registration, off by default.** Today's
`crackbox run` and `arizuko create` workflows keep SNI-passthrough.
Opting in is one bool on the register call. Reverts trivially.

b. **One CA per arizuko instance, not shared across instances.** Each
instance gets its own `crackbox-ca/`. Cross-instance secret leakage
via shared CA is impossible. Trade: every instance must distribute
its own CA to its containers (the image is per-instance anyway).

c. **MITM is a broker middleware caller, not a parallel resolver.**
Spec 6/Y is canonical for secret resolution; this spec imports the
broker's `injectSecrets` middleware function and lists it in the
chain. egred holds no placeholder map, no `secret_ref` strings, no
in-process secret cache. Per-source `WireEntry` carries `secret_scope`
(visibility), nothing more. One resolver in the system, two callers
(MCP dispatch, MITM data path).

d. **MITM data path uses the same `Chain(slice, terminal)` idiom as
proxyd.** Middlewares are `func(http.Handler) http.Handler` slice
elements, reduced with `Chain`. Order is auditable, new middleware
lands in one slot. Cross-reference
`specs/5/6-middleware-pipeline.md ## 6/6c HTTP chain`.

e. **Reuse `secret_use_log`, no new audit table.** Spec 6/Y already
built this. `caller='egred'` discriminates MITM swaps from broker
swaps. Same operator analytics work.

f. **Client-to-egred MITM is HTTP/1.1 only in v1; HTTP/2-required
hosts use `bypass_mitm`.** The MITM listener advertises `http/1.1`
only. Upstream side is whatever upstream prefers. Real h2 MITM is
deferred; operationally, h2-only destinations are registered under
`bypass_mitm` so the client negotiates h2 directly with upstream.

g. **We do NOT vendor iron-proxy.** We port the small surface we need
(~310 LOC across cagen + leafcache + the MITM listener entrypoint)
and own its lifecycle. Their YAML, gRPC management API, 1Password
resolver, host-match rules, and transform pipeline all stay in their
repo. Boring code: we won't carry a dep we use 6% of.

h. **Escape hatches are per-source `bypass_mitm` SNI rules, not
per-request headers.** `WireEntry` carries `bypass_mitm: []string`
with exact-host and `*.` wildcard patterns. `handleConnect` evaluates
the CONNECT authority before any TLS and chooses raw passthrough when
a rule matches. There is no `X-Crackbox-No-MITM` header in v1. Detail:
`## Escape hatches`.

i. **Pinned-cert and custom-`tls.Config` clients are handled by
hostname bypass, not process patching.** If a client rejects the
arizuko CA because it pins roots, pins SPKI, or constructs its own
trust pool, the operator lists the upstream hostname in `bypass_mitm`.
We do not attempt `LD_PRELOAD` or runtime TLS interception of Go or
Rust binaries.

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
