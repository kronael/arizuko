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

crackbox/pkg/admin/registry.go            entry grows MITM bool + BypassMITM + Placeholders
crackbox/pkg/admin/api.go                 WireEntry grows the same three fields
crackbox/pkg/proxy/proxy.go               handleConnect calls shouldMITM(entry, host)
crackbox/cmd/crackbox/main.go             new `ca init` subcommand wraps cagen
```

Per-source registration shape (extends today's `WireEntry`):

```go
type WireEntry struct {
    IP           string            `json:"ip"`
    ID           string            `json:"id"`
    Allowlist    []string          `json:"allowlist"`
    MITM         bool              `json:"mitm,omitempty"`
    BypassMITM   []string          `json:"bypass_mitm,omitempty"`
    Placeholders map[string]string `json:"placeholders,omitempty"`
}
```

`Placeholders` maps the placeholder _literal_ (`"$ANTHROPIC_API_KEY"`)
to a **secret_ref** (`"user:alice-sub:ANTHROPIC_API_KEY"` or
`"folder:corp/eng:ANTHROPIC_API_KEY"`). The resolver parses the ref
and calls `store.LookupSecret(scope, scopeID, key)` at request time.
No values land in the registry on disk — only references do.
`BypassMITM` holds per-source SNI rules evaluated before TLS — see
`## Escape hatches` for the matcher and operator guidance.

CONNECT flow with MITM on (rough):

```
1. Client (in container 10.99.0.42) sends CONNECT api.anthropic.com:443
2. egred handleConnect: src=10.99.0.42 → registry.Lookup → entry
3. shouldMITM(entry, "api.anthropic.com") = true (entry.MITM && no bypass match)
4. egred returns "HTTP/1.1 200 Connection Established\r\n\r\n" to client
5. egred wraps the hijacked conn in tls.Server{ GetCertificate: leafcache.GetOrCreate }
6. Client TLS-handshakes against egred (sees leaf signed by arizuko-ca)
7. egred reads HTTP/1.1 request off the decrypted conn
8. headers.SwapPlaceholders(req, entry.Placeholders, resolver)
9. egred dials upstream (real TLS to api.anthropic.com:443), forwards request
10. Response streamed back through the same decrypted/re-encrypted seam
```

If `shouldMITM` returns false (no `entry.MITM`, or a `bypass_mitm`
rule matches the CONNECT host), egred skips steps 5–8 and falls back
to today's raw `io.Copy` splice.

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

| Client                               | Trust mechanism                          | What to set                                                                                                           |
| ------------------------------------ | ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Go `net/http`                        | System trust (`/etc/ssl/certs`)          | `update-ca-certificates` after copying to `/usr/local/share/ca-certificates/`                                         |
| `curl`, `wget`                       | System trust                             | Same.                                                                                                                 |
| Node.js (`fetch`, `https`)           | Bundled, plus `NODE_EXTRA_CA_CERTS`      | `ENV NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/arizuko-ca.crt`                                             |
| Python `requests`                    | `certifi` bundle                         | `ENV REQUESTS_CA_BUNDLE=/usr/local/share/ca-certificates/arizuko-ca.crt`                                              |
| Python `urllib`, `httpx`             | OpenSSL default + `SSL_CERT_FILE`        | `ENV SSL_CERT_FILE=/usr/local/share/ca-certificates/arizuko-ca.crt`                                                   |
| Ruby `Net::HTTP`                     | OpenSSL default + `SSL_CERT_FILE`        | Same as above.                                                                                                        |
| Rust `rustls` (native-roots)         | `rustls-native-certs` reads system trust | `arizuko-ca-sync` writes the CA into `/usr/local/share/ca-certificates/` at container start. See `## Escape hatches`. |
| Rust `rustls` (webpki-roots)         | Bundled root set, ignores system trust   | No env or trust-store knob helps. Register the host under per-source `bypass_mitm` — see `## Escape hatches`.         |
| Go programs with custom `tls.Config` | Hard-coded `RootCAs` ignores system      | Register the host under per-source `bypass_mitm` — see `## Escape hatches`. No runtime patching.                      |

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
   CA cert (read-only) at `/run/arizuko/ca/ca.crt` and the bootstrap
   runs `arizuko-ca-sync` (shipped in `arizuko-ant/cmd/arizuko-ca-sync`)
   which copies the cert into `/usr/local/share/ca-certificates/arizuko-ca.crt`,
   runs `update-ca-certificates`, and writes the env-var fan
   (`NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE`) into
   the container's env file. CA rotation propagates on the next spawn
   without an image rebuild.
4. **For the proxy itself**, the container env already sets
   `HTTPS_PROXY=http://egred:3128`. No change.

`arizuko-ca-sync` fixes native-root rustls (`rustls-native-certs`) and
every other client that reads the system trust store. It does **not**
fix bundled-root rustls (`webpki-roots`) or any pinned-cert client;
those are handled by `bypass_mitm` — see `## Escape hatches`.

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
- **Time-to-leak**. From client TLS handshake to upstream dial, the
  plaintext secret lives in process memory for ~milliseconds. A core
  dump during that window leaks. Run egred without dump-permitting
  ulimits and consider `prctl(PR_SET_DUMPABLE, 0)`.
- **CA rotation breaks in-flight containers**. New CA takes effect on
  the next container spawn via `arizuko-ca-sync` (no image rebuild
  needed). In-flight containers continue trusting the old CA until
  respawn. v1 has no hot-rotation inside a live container; document
  the 30 d leaf expiry and the ~yearly CA rotation cadence in
  `SECURITY.md`.

## Escape hatches

The v1 escape hatch is decided at source registration time, not per
request. CONNECT has to choose passthrough or MITM before any TLS
bytes move, so arizuko adds `bypass_mitm` to the registered
`WireEntry` and removes the request-header design entirely.

`WireEntry` becomes:

```go
type WireEntry struct {
    IP           string            `json:"ip"`
    ID           string            `json:"id"`
    Allowlist    []string          `json:"allowlist"`
    MITM         bool              `json:"mitm,omitempty"`
    BypassMITM   []string          `json:"bypass_mitm,omitempty"`
    Placeholders map[string]string `json:"placeholders,omitempty"`
}
```

`BypassMITM` is a per-source SNI rule set. Entries are exact hosts
(`"api.openai.com"`) or a single-label wildcard prefix
(`"*.googleapis.com"`). Matching is case-insensitive on the normalized
hostname with no trailing dot. IP literals never match wildcard rules.

The CONNECT path decides before TLS. `crackbox/pkg/proxy/proxy.go`
reads the authority host from `CONNECT host:443`, looks up the source
entry, evaluates `bypass_mitm`, and only then chooses `io.Copy` splice
or `mitm.ServeTLS`.

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
created. The runner already knows the per-source allowlist and
placeholder map; it now also sends `bypass_mitm` in the same
`POST /v1/register` body to egred `:3129`. Operators extend the list
through the same arizuko-side surface; no in-band agent API is added.

**Rustls — `arizuko-ca-sync` at container start.** Shipped in
`arizuko-ant/cmd/arizuko-ca-sync`, copied into the image at
`/usr/local/bin/arizuko-ca-sync`, run by the spawn bootstrap before
the agent command. It reads the mounted instance cert from
`/run/arizuko/ca/ca.crt`, writes it to
`/usr/local/share/ca-certificates/arizuko-ca.crt`, runs
`update-ca-certificates`, and writes `NODE_EXTRA_CA_CERTS`,
`REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE` to the container env. Chose
install-time trust patch over per-app wrappers (don't cover arbitrary
subprocess trees) and over explicit-bypass-only (would throw away
working native-root clients). Fixes `rustls-native-certs` and any
client that reads the system store; does NOT fix `webpki-roots` or
embedded root sets — those use `bypass_mitm`.

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
`crackbox/pkg/mitm/listener.go` advertises
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

- `arizuko-ant/cmd/arizuko-ca-sync/main.go` — the helper.
- `container/runner.go` — mounts `/srv/data/store/crackbox-ca/ca.crt`
  read-only at `/run/arizuko/ca/ca.crt` and invokes the helper in the
  container bootstrap.
- `crackbox/pkg/admin/api.go` + `registry.go` — persist `BypassMITM`.
- `crackbox/pkg/proxy/proxy.go` — calls `shouldMITM(entry, connectHost)`.
- `crackbox/pkg/mitm/listener.go` — `NextProtos: []string{"http/1.1"}`.
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

| Bucket                                                                                  | LOC         |
| --------------------------------------------------------------------------------------- | ----------- |
| `crackbox/pkg/mitm/cagen.go` (port of iron-proxy/internal/cagen)                        | ~140        |
| `crackbox/pkg/mitm/leafcache.go` (port of iron-proxy/internal/certcache)                | ~170        |
| `crackbox/pkg/mitm/listener.go` (hijacked-CONNECT → tls.Server, new)                    | ~120        |
| `crackbox/pkg/mitm/headers.go` (placeholder swap, adapt from secrets pkg)               | ~80         |
| `crackbox/pkg/mitm/secrets.go` (resolver interface + HTTP impl)                         | ~60         |
| `crackbox/pkg/admin/api.go` + `registry.go` deltas (MITM + BypassMITM + Placeholders)   | ~40         |
| `crackbox/pkg/proxy/proxy.go` deltas (`shouldMITM` + CONNECT branch + `sniMatch`)       | ~80         |
| `crackbox/cmd/crackbox/main.go` (`ca init` subcommand)                                  | ~50         |
| Tests (CA gen, leaf mint, header swap, `sniMatch`, end-to-end with real TLS)            | ~280        |
| `ant/Dockerfile` (CA install + env vars + helper binary)                                | ~10         |
| `arizuko-ant/cmd/arizuko-ca-sync/main.go` (container-start trust helper)                | ~80         |
| `container/runner.go` (mount CA + bootstrap helper + placeholder + bypass map on spawn) | ~60         |
| arizuko-side resolver HTTP handler in `gated`                                           | ~60         |
| `store/migrations/0065-egred-mitm.sql` (extend allowlist persistence?)                  | ~10 (see ↓) |
| **Total net new in Go**                                                                 | **~895**    |

The migration is small: per-source `Placeholders` map and `BypassMITM`
list live in the admin registry's existing JSON state file
(`CRACKBOX_STATE_PATH`). No new SQL table needed. `secret_use_log`
(0048) already exists from 6/Y.

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

e. **Client-to-egred MITM is HTTP/1.1 only in v1; HTTP/2-required
hosts use `bypass_mitm`.** The MITM listener advertises `http/1.1`
only. Upstream side is whatever upstream prefers. Real h2 MITM is
deferred; operationally, h2-only destinations are registered under
`bypass_mitm` so the client negotiates h2 directly with upstream.

f. **We do NOT vendor iron-proxy.** We port the small surface we need
(~310 LOC across cagen + leafcache + the MITM-listener entrypoint)
and own its lifecycle. Their YAML, gRPC management API, 1Password
resolver, host-match rules, and transform pipeline all stay in their
repo. Boring code: we won't carry a dep we use 6% of.

g. **Escape hatches are per-source `bypass_mitm` SNI rules, not
per-request headers.** `WireEntry` carries `bypass_mitm: []string`
with exact-host and `*.` wildcard patterns. `handleConnect` evaluates
the CONNECT authority before any TLS and chooses raw passthrough when
a rule matches. There is no `X-Crackbox-No-MITM` header in v1. Detail:
`## Escape hatches`.

h. **Rust trust bootstrap ships as `arizuko-ca-sync` at container
start.** The helper installs the per-instance CA into the container
trust store and rewrites the env-backed CA hints on every spawn so CA
rotation does not depend on image rebuilds. This improves native-root
clients but does not claim to fix bundled-root rustls.

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
