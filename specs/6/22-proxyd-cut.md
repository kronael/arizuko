---
status: draft
depends: [16-daemon-standalone-matrix, ../5/7-proxyd-standalone]
---

# specs/6/22 — cut proxyd to its irreplaceable half

`6/16` already states the position: proxyd is "the commodity half —
Caddy/Traefik plus Authelia's job, and the weakest position in the repo",
and the move is "shrink toward off-the-shelf and publish the
grant-scoped mount contract, not extract a bundle."

**This spec does not remove proxyd.** It states what proxyd is actually
made of, and orders the cuts so each one stands alone. Step 1 is worth
doing whether or not an off-the-shelf proxy is ever adopted.

## What is in there

1,631 lines of non-test Go, and **3,955 lines of test** — more than two
lines of test per line of code, most of it exercising hand-rolled
proxying. That ratio is the strongest argument in this document: it is
the cost of owning a commodity.

Four jobs live in one daemon:

| Job                               | Verdict                                                                                                                                                                                  |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| route matching + reverse proxying | commodity. Caddy and Traefik do it, better tested                                                                                                                                        |
| TLS termination                   | commodity, and Caddy does automatic certs                                                                                                                                                |
| folder + `acl` scoping            | **irreplaceable** — `auth.MatchSlot`, per-folder mounts, no parent inheritance (`5/10`). No off-the-shelf proxy has a folder-agent                                                       |
| bespoke token handlers            | **misfiled** — `/chat/<token>` (`dispatchRouteToken`), `/hook/<token>`, and the vhost redirect (`worldForHost`/`vhostRedirect`, `main.go:498-530`) are application surface, not proxying |

## Step 1 — move the misfiled handlers out (do this first)

`/chat/<token>` and `/hook/<token>` terminate in proxyd: they resolve a
route token, mint identity and dispatch. That is `webd`'s job — the same
token-chat, SSE and `/hook` surface `6/16` already calls "the agent
surface, not a web-stack part". The vhost redirect is instance branding.

None of this is proxying, and none of it depends on the proxy decision.
Moving it makes proxyd honest **either way**: if the off-the-shelf swap
never happens, proxyd is still a proxy rather than a proxy with an
application stapled to it.

Cost: a relocation, not a rewrite. The handlers keep their logic, their
tests move with them, and `proxyd_routes` gains ordinary rows pointing at
webd instead of proxyd matching the paths itself.

## Step 2 — routes become rows (blocked on `L45`, signed off)

Config for any off-the-shelf proxy must be **rendered** from somewhere.
It cannot be rendered from `PROXYD_ROUTES_JSON`, which is seed-once and
ignored after first boot — the fault `L45` records, and the reason krons
is missing 7 of the 18 routes arizuko ships.

With routes as rows, the proxy config becomes one more reader of one
declaration (root `CLAUDE.md`, "one declaration, many readers"). The
renderer is small: read rows, marshal the proxy's config object, POST it
to Caddy's admin API (`/load`) or drop a file for Traefik's provider.
Estimate ~150–250 lines.

## Step 3 — the auth split (not yet signed off)

What stays arizuko's becomes a **forward-auth endpoint**: the proxy makes
a subrequest per request; arizuko answers 200 plus `X-User-*`, or 401.
This is Traefik `ForwardAuth` / Caddy `forward_auth` / nginx
`auth_request`.

Two constraints that decide whether this is safe:

- **The transit proof must be minted per request, in the auth response.**
  Today `auth.ProxydTransit` proves the request came through proxyd, and
  backends verify that bearer before trusting `X-User-*`. If the proxy
  instead holds a long-lived token in its config, that is a downgrade.
  Returning a freshly minted bearer as a copied response header preserves
  the current property.
- **Authorization is never cached.** One subrequest per request, always —
  a cached authz decision is a revocation that does not take effect. On
  localhost the cost is a fraction of a millisecond; it is still a real
  per-request tax where today the check is in-process.

## What this spec does NOT propose

Removing proxyd, choosing between Caddy and Traefik, or moving TLS. Those
follow step 3 and are decided then, on evidence from steps 1 and 2.

## Ties

`6/16` (the position, and the four-layer map) · `5/7` (proxyd standalone —
already specs it droppable) · `5/10` (per-folder mounts, no parent
inheritance — the irreplaceable mechanism) · BUGS `L45` (routes as rows,
step 2's prerequisite) · `L44` (packages declare through the manifest).
