---
status: partial
---

# authd — central authority daemon + offline-verify library

> **Status (2026-08-06).** Partial. authd's dashd cockpit tile is declared
> `Built:false`, so the sole signer has no operator surface; and nothing lets an
> operator revoke another user's refresh-token family — `revokeFamily` fires
> only from reuse detection and from the user's own `/auth/logout`. The tile
> cannot honestly flip: authd publishes **no** admin API to render or drive.
> There is no signing-key metadata endpoint (the JWK Set drops `active` /
> `created_at` / `retired_at`), no session or refresh listing (one query reads
> `refresh_tokens`, keyed by `token_hash`), `RevokeAllNow` has zero
> callers, and `serviceGrants` has no `service:dashd` entry. Building it needs
> three decisions — who may revoke whose session, which DB the audit row lands
> in (authd audits into `auth.db`; `/dash/audit/` reads `routd.db`), and resreg
> resource vs hand-rolled. Proposal in BUGS `F15a`, awaiting sign-off. BUGS `F15`.

**DECISION.** Token authority is centralized in one `authd` daemon — the **sole
signer**. It holds the ES256 private key, publishes public JWKs at `/v1/keys`,
runs the OAuth login flow, and mints every token. Every other daemon
**offline-verifies** against cached JWKs via the `auth/` library; none mint.

Why not distributed/self-minting: verification is then a pure function over
`(token, JWKs)` — no per-request hop, and authd being briefly down does not stop
verification of already-issued tokens. One issuer is also the one place to
record issuance and rotate the signing key, which is the emergency-revoke lever.

HMAC identity is fully retired — `PROXYD_HMAC_SECRET` and `CHANNEL_SECRET` are
gone. The only symmetric secrets left are the OAuth CSRF-state HMAC (a CSRF
token, not identity — `auth/oauth.go` `SignState`/`VerifyState`) and the
per-daemon service bootstrap secrets (exchange credentials, not token signers).

Two artifacts:

- **`authd`** — the daemon. ES256 private key, `auth.db` + its own
  `migrations/`, the OAuth login flow, token issuance, refresh rotation, JWKs
  publication.
- **`auth/`** — the library. Offline verification, scope check, JWKs cache,
  OAuth primitives. It IS authd's published client contract; there is no
  separate `authd/api/v1` package.

## Crypto stack (LOCKED)

We are an internal token mint, not a public OAuth/OIDC authorization server.
Three libraries, one shared JOSE implementation:

| Library                         | Role                                                                                                |
| ------------------------------- | --------------------------------------------------------------------------------------------------- |
| `golang.org/x/oauth2`           | Login code-exchange against Google/GitHub/Discord token endpoints.                                  |
| `github.com/coreos/go-oidc/v3`  | OIDC relying-party verify of Google's `id_token`; `RemoteKeySet` for JWKs fetch/cache in verifiers. |
| `github.com/go-jose/go-jose/v4` | ES256 sign in `authd`; marshal the public JWK Set. Rides transitively under go-oidc.                |

Only Google is a true OIDC provider (returns an `id_token`); GitHub, Discord,
and Telegram resolve identity via userinfo endpoints / widget HMAC
(`authd/oauth.go`, primitives in `auth/oauth.go`). Email-allowlist and GitHub-org
gates carry forward.

`auth/` never imports go-jose directly. For arizuko-issued tokens it runs a
plain JWT verify (parse, select key by `kid`, check signature + `iss`/`exp`/
`nbf`) — **not** `IDTokenVerifier`, which is `id_token`-only and used solely at
Google login.

**Rejected:** hand-rolled JWT/JWKS (HS256, no `kid`, no rotation) — exactly what
go-jose + go-oidc remove; `ory/fosite` / `zitadel/oidc` — full authorization-server
frameworks (consent, client registration, grant-type state machines) at ~20× our
scope.

## Revocation = short-TTL only (LOCKED)

**No revocation list, no feed.** Verifiers stay fully offline and never learn
per-token revocation. Three cases cover everything:

- **Normal revoke** — wait for natural expiry (~15 min access TTL bounds the
  blast radius).
- **Revoke a refresh token** (logout, sign-out-everywhere) — delete/revoke its
  row. The access token it would have refreshed still works to its own `exp`; no
  new ones issue.
- **Emergency revoke** (key compromise) — rotate the signing key with zero
  overlap. Every token signed by the retired `kid` fails once verifiers refresh
  the JWK Set.

## Sessions — short access JWT + rotating refresh token (LOCKED)

1. **Access JWT** — ES256, `typ:"user"`, ~15 min, verified offline, carried as
   `Authorization: Bearer`.
2. **Refresh token** — opaque 256-bit string (not a JWT), ~30 days, held at
   `authd` as a SHA-256 hash, delivered as an `HttpOnly` cookie.

**Rotation is one-time-use.** Every refresh consumes the presented token and
issues a successor; the consumed row is tombstoned (`used_at`), not deleted.
Presenting an already-used token is a theft signal: authd revokes the entire
`family_id` chain and returns 401.

## auth.db

authd owns `auth.db` and migrates it from `authd/migrations/*.sql`, keyed
`service="authd"` in the shared `migrations` table so its numbering is
independent of `store/`. Schema is `authd/migrations/0001-authd-schema.sql`
(`signing_keys`, `auth_users`, `oauth_identities`, `refresh_tokens`).
`0004-identities.sql` added an advisory `identities`/`identity_claims`
cross-channel-claim axis, never populated by any live writer; `0006` dropped
both tables 2026-08-04 (`fcd845cb`) — binding a channel identity to a person is
[`5/31`](31-identity-pairing.md) pairing now.

`signing_keys` validity is **time-based, no `revoked` flag**: a key serves while
`active` OR `now < retired_at + maxAccessTTL`. Emergency revoke backdates
`retired_at` so the serving window is already closed and the kid drops from the
JWK Set at once, then ages out by normal GC.

<!-- UNVERIFIED: private-key encryption-at-rest (AUTHD_KEY_ENCRYPTION_KEY,
     "plain:"/"gcm:v1:" tagged envelope) is specced but NOT built — no such env
     var or prefix exists in the tree. Today the DB file is the trust boundary.
     Same for local-password login (users.username + argon2id), the oauth_state
     table (state is a signed cookie instead), the internal_keys/collide_hmac
     row, and the /auth/collide collision screen. -->

## JWT claim set

Every minted token is an ES256 JWS with a **required** `kid` header (verifiers
select the public key by it). Claims, mint input, and verify output are one
shape — `auth.Subject` (`auth/es256.go:120`): `sub`, `typ`, `scope`, `aud`,
`iss` (pinned `"authd"`), `jti`, `parent_jti` (downscoped only), `iat`/`nbf`/
`exp`, and `Extra` for app-specific claims.

- `typ` is a **claim** (`"user" | "service" | "downscoped"`), distinct from the
  JWS header `typ:"JWT"`. It drives verifier policy — a `downscoped` token must
  carry `parent_jti`.
- `scope` is namespace-wildcard-capable (`tasks:*`) but **never** global `*:*`
  ([`17-openapi-mcp.md`](17-openapi-mcp.md) § Auth model). Match logic:
  `auth.HasScope` (`auth/scope.go:13`).
- `arz/folder` is the namespaced folder claim, kept as a private claim so
  `auth/` stays domain-agnostic; it surfaces as `Extra["folder"]`.
- 30s clock-skew tolerance on `nbf`/`iat`/`exp`.
- **`sub` prefix rule (pinned).** The `user:`/`service:` prefix appears **only**
  in the JWT `sub` claim. The **bare** canonical sub is stored everywhere else —
  all DB columns, the grants lookup, the migration mapping. authd strips the
  prefix when calling grants and ingesting `caller_sub`; it adds it only when
  stamping the claim.

There is no `tier` field — scopes replace tier everywhere ([`5/33`](33-paths-roles.md)).

## Account linking + collision rules

One canonical user may have many provider identities. `(provider, provider_sub)`
is globally unique (one external identity → at most one user) and
`(user_id, provider)` is unique (one link per provider per user, so
unlink-by-provider is unambiguous).

- **First login** — create the user row, insert the identity, issue a session.
- **Returning login** — resolve `(provider, provider_sub)` to its user.
- **Explicit link** (`intent=link`, carried in the signed state) — new identity
  attaches to the current user; an identity **already linked to a different
  user** is a **hard fail**. We never auto-merge two populated canonical users;
  merging is an operator action, out of scope.
- **Auto-link-by-verified-email: NO** — account-takeover vector if one
  provider's email verification is weaker. Linking is always explicit or
  first-login. Email is recorded for audit + the allowlist gate only.

## `/v1/*` wire surface

**DECISION (route naming).** Machine token/key endpoints live under `/v1/*`; the
human OAuth browser flow keeps `/auth/*` (proxyd 302s to authd —
[`7-proxyd-standalone.md`](7-proxyd-standalone.md) § Login flow). The two
prefixes do not overlap. `GET /v1/keys` is **public**, mounted before auth
middleware; everything else needs a bootstrap secret or a bearer. JSON errors
are `{"error":"<code>","message":"<human>"}`.

Routes: `authd/http.go:93-98`. OAuth routes: `authd/oauth.go:53-70`.

- **`GET /v1/keys`** — the JWK Set, marshalled from rows that are active or
  within their overlap window. `Cache-Control: public, max-age=3600`; verifiers
  also refresh once on a `kid` miss before failing (go-oidc behavior).
- **`POST /v1/tokens`** — one endpoint, two modes, distinguished by whether the
  caller holds `tokens:mint` for a **different** `sub`. This is the only
  authority rule that matters, so it is stated per mode:
  - **Issuer mint** (`authd/server.go:241` `IssuerMint`; onbod, dashd,
    proxyd-on-login) — the minted scope is bounded by the **target sub's grants
    snapshot**, not by the caller's own scope: login and invite flows mint USER
    tokens for accounts whose grants the minter does not itself hold. The
    caller's `tokens:mint` is authority to mint _at all_; the target's grants set
    the ceiling. Violation → `403 scope_exceeds_minter`.
  - **Downscope** (`authd/server.go:214` `Downscope`; any valid bearer) — same
    `sub`, scope ⊆ the **caller's** scope, folder within the caller's subtree,
    `parent_jti` = caller's `jti`, TTL capped at the parent's remaining
    lifetime. Violation → `403 scope_exceeds_parent`.
- **`POST /v1/service-token`** — a daemon exchanges its bootstrap secret
  (Authorization header, kept out of body logging) for a short service JWT with
  `sub = service:<daemon>`. Constant-time hash compare over every configured
  secret, so neither a wrong daemon name nor a wrong secret leaks timing
  (`authd/http.go:172` `matchServiceSecret`).
- **`POST /v1/refresh`** — consumes and rotates the refresh token, returning the
  successor **by the same channel it was presented on**: cookie in → successor
  in `Set-Cookie` (stays `HttpOnly`, omitted from the body); JSON body in →
  successor in the body, no cookie. If both are present the cookie wins. Re-runs
  the grants snapshot so a refreshed token reflects current grants.
- **`GET /v1/identities/{sub}`** — the advisory cross-channel identity read.
- **`POST /v1/keys/rotate`** — **deferred, unbuilt** along with the `authd
rotate-key` CLI. The rotation _mechanism_ below is the only emergency-revoke
  lever; until the endpoint lands, short-TTL expiry plus a redeploy with a fresh
  key rotates it.

### Login-time scope snapshot

authd does not own grants — routd does. At session issuance authd fetches
`GET <GRANTS_URL>/v1/users/{bare-sub}/scopes` with its own `service:authd` token
and stamps the result into `scope` + `arz/folder`
(`authd/oauth.go:326` `snapshot`). authd **self-mints** that token — it holds the
signing key, so it needs no bootstrap secret; the seed mechanism exists for the
other daemons, which lack the key.

- Snapshot is taken **once at issuance**; later grant changes apply at the next
  refresh or login (the short-TTL model).
- **Fail modes are asymmetric on purpose.** `404 no_grants` → mint an
  empty-scope session (authenticated but unauthorized; the browser lands on
  `/onboard`). A 5xx → login **fails closed**, no token minted, rather than
  masking an outage as an empty-scope session.
- `GRANTS_URL` unset (a standalone auth-only deployment) makes every session
  empty-scope.

## JWK rotation mechanics

- **`kid`** is `"<created-unix>-<8 hex rand>"` — sortable, collision-resistant,
  written into every token's JWS header.
- **Startup-if-missing** — no active key on boot ⇒ generate one. First boot
  needs no operator step.
- **Overlap window** — rotation inserts a new active key and sets the old one's
  `retired_at`; both public halves stay in `/v1/keys` until
  `retired_at + maxAccessTTL`, so old-`kid` tokens verify until they would have
  expired anyway. GC drops the row after.
- **Emergency revoke** — retire with `retired_at` already in the past
  (`authd/server.go:108`), so the serve check is false immediately and the kid
  drops now. Every token it signed fails within one JWKS cache TTL. The single
  lever that invalidates everything at once.

## TTLs

Access 15 min, refresh 30 days, max-access 1 hour (the retired-key serving
bound) — hardcoded constants at `authd/main.go:25-27`. JWKS verifier cache is
go-oidc's `RemoteKeySet` default plus refresh-on-`kid`-miss. Downscoped tokens
are capped at the parent's remaining lifetime.

<!-- UNVERIFIED: the AUTHD_ACCESS_TTL / AUTHD_REFRESH_TTL / AUTHD_SERVICE_TTL /
     AUTHD_STATE_TTL / AUTHD_JWKS_CACHE_TTL / AUTHD_KEY_ROTATION_DAYS env
     overrides this spec once tabled do NOT exist; nor does scheduled rotation.
     Rotation is manual-only today. -->

## Service bootstrap

Daemon-initiated work (timed firing a task, onbod admitting, a cron sweep) has
no user in the loop but needs an identity to call routd. It gets a **service
identity** — `sub = service:<daemon>` plus that daemon's capability scope — which
verifies exactly like a user token. No second path.

- **Secrets** live in env, not a table: compose generation writes each daemon's
  own `AUTHD_SERVICE_KEY` into its container env and the full
  `principal=secret` set into authd's `AUTHD_SERVICE_KEYS`
  (`authd/main.go:161` `loadServiceSecrets`). authd's own bootstrap doubles as
  its service secret so it can exchange too.
- **Scopes** per principal: `authd/http.go:26` `serviceGrants`.
- **Blast radius**: a leaked key buys exactly that one daemon's scoped token —
  never the ability to sign, since only authd holds the private key.
- **Rotation**: re-generate compose (new env secret), restart the daemon.
- The daemon-side helper is `auth.ServiceToken` (`auth/service.go:44`), which
  refreshes ahead of expiry the way `RemoteKeySet` does — no per-request hop.

**Adapters exchange as the DAEMON principal** (`AUTHD_SERVICE_NAME`, e.g.
`teled`), never as the channel name (`telegram`). The mismatch made authd 401
and outbound silently fail on a live krons instance; fixed in v0.50.0.

## Verifying daemon — the mounting pattern

A daemon fetches the JWK Set at boot (`auth.FetchKeys`, `auth/jwks.go:70`),
exchanges its bootstrap secret for a service token, and verifies incoming
bearers offline (`auth.VerifyHTTP`, `auth/jwks.go:189`). Only authd holds the
private key.

Requests that arrive **through proxyd** carry trust-stamped `X-User-*` headers,
which a backend accepts only when `auth.ProxydTransit` (`auth/middleware.go:22`)
verifies proxyd's own `service:proxyd` ES256 bearer. There is no
`RequireSigned`/`X-User-Sig` per-request signature — that was the HMAC era and
is retired. Full trust model: `SECURITY.md` § "Identity header trust".

<!-- UNVERIFIED: the auth/mcp.go `MCPTools` surface (whoami / mint_token /
     verify_token / list_providers) is NOT built — no such file, no such tool
     names anywhere in the tree. Agent → sub-agent delegation by minting a
     narrower token is therefore unavailable; the turn is credentialed by its
     SO_PEERCRED socket instead (ipc/ipc.go peerCred). -->

## What this is not

- Not distributed minting — authd is the sole signer.
- Not symmetric token crypto — ES256 from launch; daemons hold only public JWKs.
- Not a public OAuth/OIDC authorization server — no client registration,
  consent, or introspection.
- Not a per-token revocation list or feed.
- Not issuer-side OIDC conformance — we are a relying party; issued tokens are
  plain ES256 JWTs.
