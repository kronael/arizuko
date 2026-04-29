# egred

Egress daemon: transparent network proxy with per-folder allowlist.

## Purpose

Default-deny network egress for agent containers. Spec 6/10
(crackbox-arizuko). Sits as a sidecar on the per-instance Docker
internal network; the agent containers' only path out.

## Responsibilities

- Listen on `:3128` for traffic redirected by iptables REDIRECT in
  egred's own netns. Peek SNI (`:443`) or `Host:` header (`:80`),
  match against the per-source-IP allowlist, splice on allow.
- HTTP API on `:3129`: `POST /v1/register`, `POST /v1/unregister`,
  `GET /v1/state`, `GET /health`. Used by gated to inform egred when
  agent containers spawn or exit.

## Entry points

- Binary: `egred/main.go`
- Proxy listen: `EGRED_PROXY_ADDR` (default `:3128`)
- API listen: `EGRED_API_ADDR` (default `:3129`)
- iptables setup: `egred/entrypoint.sh` runs at container start
  (REDIRECTs `:80,:443` from `EGRED_INTERNAL_SUBNET` to proxy port)

## Dependencies

None (no DB; in-memory IP→folder map populated by gated).

## Configuration

| Var                     | Default        | Purpose                               |
| ----------------------- | -------------- | ------------------------------------- |
| `EGRED_PROXY_ADDR`      | `:3128`        | proxy listener                        |
| `EGRED_API_ADDR`        | `:3129`        | register/state HTTP API               |
| `EGRED_INTERNAL_SUBNET` | `10.99.0.0/16` | iptables source filter (entrypoint)   |
| `EGRED_PROXY_PORT`      | `3128`         | iptables REDIRECT target (entrypoint) |

## Files

- `main.go` — wiring, signal handling
- `proxy.go` — accept → peek → allowlist → splice
- `peek.go` — SNI extraction, HTTP `Host:` peek, byte reader
- `origdst.go` — `getsockopt(SO_ORIGINAL_DST)` (linux only)
- `allow.go` — per-IP allowlist + match (ported from crackbox)
- `api.go` — register/unregister/state HTTP handlers
- `entrypoint.sh` — installs iptables REDIRECT rules at startup
- `Dockerfile` — alpine + iptables + binary
- `Makefile`

## How traffic flows

```
agent container (10.99.0.5)
  → packet to api.anthropic.com:443
  → default route via egred's internal NIC (10.99.0.1)
  → iptables PREROUTING REDIRECT to :3128
  → egred reads SO_ORIGINAL_DST → 1.2.3.4:443
  → egred peeks ClientHello SNI → "api.anthropic.com"
  → allowlist match for source IP 10.99.0.5 → allow
  → egred dials 1.2.3.4:443 from external NIC
  → io.Copy both directions; TLS handshake completes container↔upstream
```

The proxy never decrypts. SNI peek only.

## Failure modes

- **Unknown source IP**: traffic dropped (gated didn't register, or
  agent IP changed). Closes connection without response.
- **SNI/Host parse fails**: drop. Bad TLS or non-TLS on :443.
- **Domain not in allowlist**: drop, log.
- **iptables not installed**: container startup fails (entrypoint).

## Health signal

`GET /health` returns 200. Healthy = process up + listener bound.
Does not validate iptables state (rules are set once at boot).

## Related

- `store/network.go` — `network_rules` table + `ResolveAllowlist`
- `container/egress.go` — gated-side register/unregister glue
- `compose/compose.go` — service block generation
- `specs/6/10-crackbox-arizuko.md` — spec
- `crackbox/internal/vm/{proxy,netfilter}.go` — origin of the
  allowlist matching code
