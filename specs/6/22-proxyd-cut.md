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

| Job                               | Verdict                                                                                                                                                                                                                                                     |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| route matching + reverse proxying | commodity. Caddy and Traefik do it, better tested                                                                                                                                                                                                           |
| TLS termination                   | commodity, and Caddy does automatic certs                                                                                                                                                                                                                   |
| folder + `acl` scoping            | **irreplaceable** — `auth.MatchSlot`, per-folder mounts, no parent inheritance (`5/10`). No off-the-shelf proxy has a folder-agent                                                                                                                          |
| bespoke token handlers            | **misfiled** — `/chat/<token>` (`dispatchRouteToken`) and `/hook/<token>` were application surface, not proxying. Moved to webd, step 1. The vhost redirect (`worldForHost`/`vhostRedirect`) was listed here too and does not belong: see "Not moved" below |

## Step 1 — move the misfiled handlers out (DONE for /chat/ + /hook/, 2026-08-18)

`/chat/<token>` and `/hook/<token>` terminated in proxyd: it resolved a
route token, minted identity and dispatched. That is `webd`'s job — the same
token-chat, SSE and `/hook` surface `6/16` already calls "the agent
surface, not a web-stack part".

None of this is proxying, and none of it depends on the proxy decision.
Moving it makes proxyd honest **either way**: if the off-the-shelf swap
never happens, proxyd is still a proxy rather than a proxy with an
application stapled to it.

### What shipped

`dispatchRouteToken` is gone, and with it the `switch rt.Path` arm naming
`/chat/` and `/hook/`. proxyd no longer knows either path.

- **Token resolution deleted, not moved.** webd already resolved the token in
  every handler (`lookupRouteToken`); proxyd resolved the same row a second
  time to stamp `X-Chat-Token` / `X-Folder` / `X-Group-Name`. One row, one
  reader now. `X-Group-Name` had no reader at all.
- **The anon DoS shield moved** to `webd/anonshield.go`, keyed on the
  `X-Forwarded-For` client proxyd resolves — not `RemoteAddr`, which behind
  proxyd is one address for the whole world (`L40`).
- **`auth: optional` replaces the path special-case.** proxyd still does the
  one thing only proxyd can: identify a caller who offers a bearer, and prove
  transit. It is now a route attribute, so the rows are ordinary rows.

### The bug this exposed

proxyd attached its `service:proxyd` transit bearer only inside
`setUserHeaders`, i.e. only for callers it authenticated. An anonymous
`/chat/` request therefore reached webd with no proof it had transited proxyd
— and `/chat/stream`'s gate demanded exactly that proof. **On every instance
with a JWKS configured, the chat widget's live stream answered 403 to every
anonymous visitor.** It could only pass in local dev, where a nil KeySet
short-circuits the check.

`optional` attaches the proof on both paths, which fixes it. The gate itself
survives, restated as what it always meant: `transited(r)`, the bearer, with
none of the token-stamp indirection.

One property did NOT survive: the gate used to distinguish "proxyd resolved a
token for this request" from "proxyd merely authenticated someone", so a
signed-in user whose grants missed the token's folder was refused. proxyd no
longer resolves tokens, so that distinction is unreachable by relocation. It
was nominal — the refused caller had to be holding the route token, which
already lets them POST into the chat and read its history through sibling
endpoints that have never had a gate. The test now asserts the containment
that does the work: `?group=` cannot aim the subscription (`L30`).

### Not moved: the vhost redirect

`worldForHost` / `vhostRedirect` stay in proxyd. They are not addressable by a
`proxyd_routes` row: both call sites are proxyd's _fallback_, one at bare `/`
(entangled with websocket upgrade to vited and external `PUB_REDIRECT_URL`
probing) and one at the unmatched-path catch-all. Routing either to webd means
changing what proxyd does with **every** unmatched request fleet-wide, and
makes the world front door depend on webd being up.

The redirect is also not the application surface the table above calls it: it
emits `/pub/<world>/<path>`, the same shape as the plain `/pub` fallback
beside it, into a path vited serves and webd does not. It belongs to whoever
owns the `/pub` fallback. Either amend the table's verdict, or spec the
catch-all change on its own — it is a redesign, not a relocation.

## Step 2 — routes become rows (unblocked 2026-08-17, signed off, NOT started)

Config for any off-the-shelf proxy must be **rendered** from somewhere.
It cannot be rendered from `PROXYD_ROUTES_JSON`, which was seed-once and
ignored after first boot — the fault `L45` records, and the reason krons
was missing 7 of the 18 routes arizuko ships. **`L45` was fixed 2026-08-17**:
`arizuko generate` writes the rows (`store.SyncProxydRoutes`) and proxyd's
boot path reads rows only. The prerequisite is therefore done and the
remaining work is the renderer itself, which is unbuilt.

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

## Step 4 — could a standard IdP let us DELETE authd? (proposed 2026-09-03, needs sign-off)

Step 3 asks the question for proxyd. The same question for `authd` has a
sharper answer, because the auth surface splits cleanly in two and only one
half is arizuko's.

Measured 2026-09-03 (non-test lines):

| half                    | files                                                                                                                              | lines    | who else does this                                         |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | -------- | ---------------------------------------------------------- |
| **authn — commodity**   | `auth/oauth.go`, `authd/oauth.go`, `auth/es256.go`, `auth/jwks.go`, `authd/sessions_resource.go`, `authd/signing_keys_resource.go` | **1618** | every OIDC provider: Dex, Authelia, Keycloak, oauth2-proxy |
| **authz — the product** | `auth/authorize.go`, `auth/acl.go`, `auth/delegate.go`, `auth/scope.go`                                                            | **1044** | nobody                                                     |

The second row is why this is a split and not a replacement. No standard
expresses `[!]action(param=glob)` over a folder tree with `WITH GRANT OPTION`
delegation and `5/33`'s two-axis rule — the turn's folder confers in full while
the attested sender adds only inside that folder's subtree and never through the
action lattice. That is not a policy an IdP can hold; it is the thing arizuko
sells. OPA/Rego could hold the RULES but not the model, and
that trade is its own decision.

The first row is 1618 lines of minting, verifying, rotating and session-storing
that a deployed IdP does better, and whose bugs are someone else's. Deleting it
means: authd stops issuing, and verifies an external issuer's JWT instead;
`signing_keys` and `sessions` go with it; the `service:<daemon>` token becomes a
client-credentials grant.

**Two things must survive the delete, and they are the whole risk.** The
`service:proxyd` transit proof (`auth.ProxydTransit`) is what makes `X-User-*`
trustworthy — step 3's constraint applies here verbatim. And the per-folder JWT
claim every handler binds its `folder`/`jid` params to is arizuko's, not an
OIDC claim; it has to be minted by arizuko's authz half from the IdP's subject,
which means authd does not disappear so much as shrink to a claims mapper.

**Recommendation: yes to the authn half, never the authz half.** The seam is
the one step 3 already names — a forward-auth endpoint arizuko owns, in front
of an identity it does not. Sequence it after step 3, which proves the seam on
the smaller surface first.

## What this spec does NOT propose

Removing proxyd, choosing between Caddy and Traefik, moving TLS, or picking
WHICH IdP. Those follow steps 3 and 4 and are decided then, on evidence from
the steps before them.

## Ties

`6/16` (the position, and the four-layer map) · `5/7` (proxyd standalone —
already specs it droppable) · `5/10` (per-folder mounts, no parent
inheritance — the irreplaceable mechanism) · `5/W` (the route-token surface
step 1 consolidates in webd) · BUGS `L45` (routes as rows, step 2's
prerequisite) · `L44` (packages declare through the manifest) · `L40` (the
shield keys on the client, not the hop — the property that had to survive the
move) · `L4` (folder from the row, not the JID grammar — one resolver now).
