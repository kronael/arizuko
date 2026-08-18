# egress

A forward HTTP/CONNECT proxy that allows one fixed list of hosts, for one
client. `runed` starts one per spawned container and stops it with the turn.

What it does: read the destination out of the request the client sends,
match it against the allowlist, splice or refuse. That is all of it.

What it does not do: terminate TLS, hold a CA, read request bodies, keep a
registry, serve an admin API, intercept traffic addressed elsewhere, or
filter DNS. Every one of those existed to let ONE proxy serve MANY tenants;
one proxy per sandbox needs none of them (spec
[`6/24`](../specs/6/24-crackbox-lightest.md)).

## Use

```sh
egress --listen :3128 --allow api.anthropic.com,github.com --client 10.99.4.7
```

| Flag       | Env             | Default | Meaning                                          |
| ---------- | --------------- | ------- | ------------------------------------------------ |
| `--listen` | `EGRESS_LISTEN` | `:3128` | listen address                                   |
| `--allow`  | `EGRESS_ALLOW`  | (empty) | comma-separated allowlist; a bare `*` allows all |
| `--client` | `EGRESS_CLIENT` | (empty) | the only peer IP served; empty accepts any peer  |

The allowlist is fixed for the life of the process. To change the rules,
start a new proxy — which is what a per-spawn proxy does anyway.

An empty allowlist denies everything. That is a valid rule (a folder with no
network grants), so it starts and logs a warning rather than refusing.

## Matching

`match.Host` — exact, subdomain, case-insensitive, trailing dot stripped.
A bare `*` allows everything. IP entries are skipped: filtering is by NAME,
because the name is what the client puts in the request line.

## `--client`, and why it is not a registry

The proxy serves exactly one sandbox. `--client` is the address of that
sandbox; every other peer gets a 403, whether it arrives from a neighbour on
the sandbox network or from the uplink side.

This is a bind scope, not an identity lookup. Nothing is registered, nothing
is resolved, and a second client is refused rather than served from some
other rule set. It matters because a per-spawn proxy shares a network with
the folder's other spawns, and those spawns do not all hold the same
allowlist — an elevated turn's is unrestricted.

## `/health`

`GET /health` → `200 {"status":"ok"}` once the listener is bound and the
allowlist loaded, `503 {"status":"starting"}` before. Public, no auth, like
every other arizuko daemon's.

Whoever starts a sandbox behind this proxy polls it FIRST and starts the
sandbox only on a 200. A TCP connect is not enough: it succeeds the moment
the port is bound, which can precede the handler serving.

It is answered before the `--client` check, so the starter — which is not
the client — can reach it. Proxy requests arrive in absolute form
(`GET http://host/path`), so an origin-form `GET /health` is unambiguously a
probe of this process, not traffic to forward.

## This proxy is half the enforcement

The other half is topological and lives in whoever starts the sandbox:

- the sandbox's network is `--internal` — it has no route out at all;
- the proxy is the only thing attached to both that network and an uplink;
- the sandbox is attached to the internal network ONLY.

So a sandbox that ignores `HTTP_PROXY` and opens a raw socket to an IP
literal reaches nothing — not because this proxy refuses it, but because
there is no route. Never give the sandbox a second interface, and never let
it share the proxy's network namespace (`--network container:<proxy>`): the
proxy is dual-homed by necessity, so its namespace carries the route out,
and `HTTP_PROXY` becomes advisory. Measured on a live docker host: a
container sharing a proxy's namespace fetched an unlisted host directly,
exit 0.

## Layout

```
egress/
  proxy.go        parse CONNECT, match, splice; /health
  match/          Host(allowlist, host) bool, validators
  cmd/egress/     the binary
  test/           end-to-end over real sockets
  Dockerfile, Makefile
```

## Image

`make images` at the repo root builds `arizuko-egress`. runed starts spawn
proxies from whatever `EGRESS_IMAGE` names; if that image is missing, every
spawn fails loudly rather than running unfiltered.
