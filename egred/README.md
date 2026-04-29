# egred

Egress daemon: forward HTTP/HTTPS proxy with per-folder allowlist.

## Purpose

Default-deny network egress for agent containers. Spec 6/10
(crackbox-arizuko). Sidecar on the per-instance Docker internal
network; the agent containers' only path out.

A transparent / iptables-REDIRECT design was attempted first and
rejected because Docker user-defined bridges put the host (not a
container) on the gateway IP — egred can't intercept packets it
never sees. Forward proxy with `HTTPS_PROXY` env on the agent is
~5 lines of plumbing instead.

## Responsibilities

- Listen on `:3128` for forward HTTP/HTTPS proxy traffic. Plain HTTP
  requests are forwarded after a `Host:` header allowlist check.
  HTTPS uses CONNECT tunneling — egred reads the target host:port,
  checks the allowlist, dials upstream, and splices opaquely. No
  TLS termination, no MITM.
- HTTP API on `:3129`: `POST /v1/register`, `POST /v1/unregister`,
  `GET /v1/state`, `GET /health`. Used by gated to inform egred when
  agent containers spawn or exit.

## Entry points

- Binary: `egred/main.go`
- Proxy listen: `EGRED_PROXY_ADDR` (default `:3128`)
- API listen: `EGRED_API_ADDR` (default `:3129`)

## Dependencies

None (no DB; in-memory IP→folder map populated by gated).

## Configuration

| Var                | Default | Purpose            |
| ------------------ | ------- | ------------------ |
| `EGRED_PROXY_ADDR` | `:3128` | proxy listener     |
| `EGRED_API_ADDR`   | `:3129` | register/state API |

Agent containers are configured by gated at spawn:

```
HTTP_PROXY=http://egred:3128
HTTPS_PROXY=http://egred:3128
NO_PROXY=localhost,127.0.0.1,gated,egred
NODE_OPTIONS=--require=/app/proxy-shim.js
```

The `proxy-shim.js` shipped in the ant image wires Node's built-in
fetch (which ignores `HTTPS_PROXY` by default) to use undici's
ProxyAgent. curl, wget, pip, go, npm honor the env vars natively.

## Files

- `main.go` — wiring, signal handling
- `proxy.go` — CONNECT + plain-HTTP forward, allowlist gate
- `allow.go` — per-IP allowlist + match (ported from crackbox)
- `api.go` — register/unregister/state HTTP handlers
- `Dockerfile`, `Makefile`

## How traffic flows

```
agent container (10.99.0.5)
  → fetch("https://api.anthropic.com/v1/messages")
  → undici/ProxyAgent honors HTTPS_PROXY
  → CONNECT api.anthropic.com:443 HTTP/1.1 → egred:3128
  → egred matches "api.anthropic.com" against allowlist for 10.99.0.5
  → egred dials api.anthropic.com:443 from its default-network NIC
  → egred replies "200 Connection Established"
  → io.Copy both directions; TLS handshake completes container↔upstream
```

The proxy never decrypts. Allowlist match by hostname only.

## Failure modes

- **Unknown source IP**: 403 (gated didn't register, or agent IP changed).
- **Domain not in allowlist**: 403, log.
- **Client doesn't honor proxy env**: connection times out (internal
  network has no default route to the internet). Fail-closed.

## Health signal

`GET /health` returns 200. Healthy = process up + listener bound.

## Related

- `store/network.go` — `network_rules` table + `ResolveAllowlist`
- `container/egress.go` — gated-side register/unregister glue
- `compose/compose.go` — service block generation
- `specs/6/10-crackbox-arizuko.md` — spec
- `crackbox/internal/vm/{proxy,netfilter}.go` — origin of the
  allowlist matching code
- `ant/proxy-shim.js` — Node fetch ↔ HTTPS_PROXY bridge
