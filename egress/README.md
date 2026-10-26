# egress

A forward HTTP/CONNECT proxy that allows one fixed list of hosts, for one
client. `runed` starts one per turn and stops it with the turn.

What it does: read the destination out of the request the client sends,
match the NAME against the deny list first and the allowlist second, resolve
it once, refuse the ADDRESS if it is a class no sandbox may reach, and dial
exactly that address. That is all of it.

What it does not do: terminate TLS, hold a CA, read request bodies, keep a
registry, serve an admin API, intercept traffic addressed elsewhere, or
filter DNS. Every one of those existed to let ONE proxy serve MANY tenants;
one proxy per sandbox needs none of them.

## Use

```sh
egress --listen :3128 --allow api.anthropic.com,github.com --client 10.99.4.7
```

| Flag               | Env                     | Default | Meaning                                                        |
| ------------------ | ----------------------- | ------- | -------------------------------------------------------------- |
| `--listen`         | `EGRESS_LISTEN`         | `:3128` | listen address                                                 |
| `--allow`          | `EGRESS_ALLOW`          | (empty) | comma-separated allowlist of host names                        |
| `--deny`           | `EGRESS_DENY`           | (empty) | comma-separated denylist; deny beats allow                     |
| `--allow-all`      | `EGRESS_ALLOW_ALL`      | off     | allow every host name; `--deny` and address checks still apply |
| `--client`         | `EGRESS_CLIENT`         | (empty) | the only peer IP served; empty accepts any peer                |
| `--deny-private`   | `EGRESS_DENY_PRIVATE`   | off     | also refuse destinations resolving into a private range        |
| `--allow-loopback` | `EGRESS_ALLOW_LOOPBACK` | off     | permit destinations on this host (local development)           |

The rules are fixed for the life of the process. To change them, start a
new proxy — which is what a per-spawn proxy does anyway.

An empty allowlist denies everything. That is a valid rule (a folder with no
network grants), so it starts and logs a warning rather than refusing.

## Matching

`match.Allowed` — deny first, then allow-all, then the allowlist. Entries
match exact or subdomain, case-insensitive, trailing dot stripped; a
`*.example.com` glob is one rule with its apex (a bare domain already covers
its subdomains). Deny beating allow is what makes an exception expressible:
allow `example.com`, deny `internal.example.com`, and the internal name plus
its whole subtree stays refused while the rest of the domain flows.

Filtering is by NAME, because the name is what the client puts in the
request line; blocked address classes are the address check's job (below).

## Validation

A rule that can never match reads as protection and is not, so the boundary
that accepts a list refuses it loudly (`match.ValidateLists`):
the binary exits at start naming the bad rule, and the same grammar guards
arizuko's write paths (`arizuko network allow`, the agent's `network_allow`)
so a bad rule is refused when it is written. Rejected: schemes, ports,
paths, credentials, IPs and CIDRs, wildcards other than one leading `*.`,
duplicates, and a host present in both lists.

A bare `*` is not a list entry: allow-everything is the explicit
`--allow-all` mode. Inside arizuko a folder's `*` network_rules row (and an
elevated turn's egress grant) still means allow-all — runed translates it to
`--allow-all` when it starts the spawn's proxy, so the magic string never
reaches a list.

## A refusal names its cause

The status tells a refused name from an unreachable one, so the client
behind the proxy does not ask for a grant it already holds.

| Answer                                | Cause                                                                                             |
| ------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `403 denied: host not allowed`        | the name is off the allowlist or on the deny list                                                 |
| `403 denied: <host>: blocked address` | the name passed; it resolves to a class no sandbox reaches                                        |
| `403 denied: peer`                    | the request did not come from `--client`                                                          |
| `502 upstream unreachable: <error>`   | the name and the address passed; the name did not resolve or the dial failed. Not a rule problem. |

## `--client`, and why it is not a registry

The proxy serves exactly one sandbox. `--client` is the address of that
sandbox; every other peer gets a 403, whether it arrives from a neighbour on
the sandbox network or from the uplink side.

This is a bind scope, not an identity lookup. Nothing is registered, nothing
is resolved, and a second client is refused rather than served from some
other rule set. It matters because the proxy listens on the host side, where
more than one turn's traffic could arrive, and those turns do not all hold
the same allowlist — an elevated turn's is unrestricted.

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

The other half is topological and lives in whoever starts the sandbox. In
arizuko that is runed: the guest's only interface is one tap, and the tap's
rule set accepts the guest's traffic to this proxy and drops everything else
in both directions.
A docker-based caller gets the same property with an `--internal` network
that has no route out, the proxy as the only member with an uplink, and the
sandbox attached to that network ONLY.

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
  match/          Allowed(allow, deny, allowAll, host), ValidateLists
  cmd/egress/     the binary
  test/           end-to-end over real sockets
  Dockerfile, Makefile
```

## Image

`make images` at the repo root builds the `egress` binary into the `arizuko`
image (`RUNED_EGRESS_BIN`, default `egress`). runed starts one process per
turn on the tap's host side; if the binary is missing, runed refuses to
start rather than running a turn unfiltered. A standalone
`arizuko-egress` Docker image also builds from `egress/Dockerfile`, for a
Docker-based caller outside arizuko's own turn path.

## The address check

The allowlist names hosts, and a name is not a destination. The proxy resolves
the matched name once and dials the address it got, so the address that passed
the check is the address reached — dialling the name again would resolve a
second time and the two answers need not agree.

Refused whatever the allowlist says: loopback, link-local (which is where every
cloud instance-metadata service lives, so `metadata.google.internal` is covered
by its address rather than by a name list), the IPv6 IMDS address, unspecified,
and multicast. A host is refused when ANY of its answers is blocked, so the
outcome never depends on resolver ordering.

Why here and not in the sandbox: the sandbox has no route out, so the agent
has nowhere to send a packet. This proxy is the one process on that path with
an uplink, which makes it the deputy worth confusing.

Private addresses are NOT refused by default — a per-spawn proxy shares the
compose network with the daemons, so private is an ordinary neighbour here.
`--deny-private` is for a deployment whose allowlist names nothing internal.

Nothing is decrypted. This is address hygiene, not interception; MITM stays
declined (2026-08-18).
