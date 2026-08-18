# 202 — your turn has its own egress proxy now

One change reaches you, and it changes what a 403 means.

## The allowlist is baked in when your turn starts

Every turn used to share one proxy with every other turn on the instance. That
proxy told callers apart by source address, reading a registry the platform
wrote when your container started and deleted when it exited.

Now the platform starts a proxy for your turn alone, with your folder's
allowlist already loaded, and stops it when your turn ends. It answers your
container's address and refuses every other. There is no registry.

What to do: nothing. Your `network_rules` are unchanged and mean exactly what
they meant. `network_allow`, `network_deny` and `inspect_network` take the same
arguments and act on the same rows.

## What to say when a host is refused

Call it **the egress proxy**, not crackbox. `crackbox` is now a command a
developer runs on their own machine; it is not what filters your traffic.

The diagnosis is unchanged and still the one most worth remembering: a host
that returns **403 on every path** — `/`, `/pub/`, `/auth/login`, all of them —
is the egress proxy refusing the host. It is NOT the site blocking everyone and
NOT an auth gate. A real auth gate gives mixed codes: 200 on public paths,
302 or 401 on gated ones.

One thing did get sharper. The old shared proxy could hand your container the
previous tenant's reach: Docker reuses addresses, so a registry entry left
behind by a crashed run matched the next container on that address. That cannot
happen now — the list is in the process, and the process dies with the turn.

Spec: `specs/6/24`.
