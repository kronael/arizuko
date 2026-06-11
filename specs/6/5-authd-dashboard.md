---
status: draft
depends: [1-cockpit-index]
---

# authd dashboard — keys, tokens, providers, identities

Architecture, routing, auth, theme per [`6/1`](1-cockpit-index.md).
This spec adds only authd's pages + show/control matrix.

**Sensitivity rule (page-wide): metadata only, never secret material.**
The dashboard never renders `priv_pem` (signing keys), raw refresh
tokens (only sha256 hashes are stored anyway — `authd/store.go:212`
`hashToken`), service bootstrap secrets, or OAuth client secrets. The
one exception is a freshly issued access JWT on the issue-token page:
shown exactly once in the mint response, never persisted, never
re-displayable.

## Purpose

Operate the trust root: watch signing keys age, rotate them, kill
refresh-token families, see who is linked to what — without `sqlite3
auth.db` on the host.

## Pages

| Page         | Route                    |
| ------------ | ------------------------ |
| overview     | `/dash/authd/`           |
| signing keys | `/dash/authd/keys`       |
| tokens       | `/dash/authd/tokens`     |
| providers    | `/dash/authd/providers`  |
| identities   | `/dash/authd/identities` |

## Show

**overview** — signer status (active key kid + age; `genKid` embeds
the creation unix time, `authd/server.go:97`, so age derives from the
kid alone); serving-key count (`Authd.serving`, `authd/server.go:36`);
refresh families live/spent/revoked counts; configured service
principals (count from `loadServiceSecrets`, `authd/main.go:158`) with
their declared scopes (`serviceGrants`, `authd/http.go:26`); grants
fetcher wired or not (`GRANTS_URL` — unset means every session is
empty-scope, `authd/main.go:69`); TTL constants (access 15m, refresh
30d, max-access 1h — `authd/main.go:24`).

**keys** — table of `signing_keys` rows
(`authd/migrations/0001-authd-schema.sql`): kid, created_at, age,
active flag, retired_at, serving-window end (`retired_at +
maxAccessTTL` — the time-based validity model, no revoked column).
Detail pane per key: public PEM (`pub_pem` is public material, fine to
show), tokens it can still verify until. Never `priv_pem`.

**tokens** — access JWTs are stateless (no issued-token table; `jti`
is not persisted), so "issued tokens" means **refresh families**:
`refresh_tokens` grouped by `family_id` — sub, scope, issued_at,
expires_at, family state (live / spent / revoked via `used_at` /
`revoked_at` tombstones). Reuse-revoked families surface here — that
row is the forensic trace of a replay
(`authd/server.go:281` `Refresh`, `errReuse`).

**providers** — one row per OAuth provider with config **presence**
(client-id set / unset — never the secret): Google, GitHub, Discord,
Telegram widget (`registerOAuth` mounts conditionally per configured
provider, `authd/oauth.go:38`). Show the allowlist guards when set
(`GITHUB_ALLOWED_ORG`, `GOOGLE_ALLOWED_EMAILS`) and `AUTH_BASE_URL`
(unset = the whole `/auth/*` surface is unmounted).

**identities** — two distinct table families, shown separately:

1. **Canonical users + provider links** — `auth_users` joined to
   `oauth_identities` (`authd/store.go:108` `upsertOAuthUser`): user_id
   (bare sub), name, created_at, linked providers with provider_sub +
   linked_at.
2. **Cross-channel identity claims** — `identities` +
   `identity_claims` (`authd/migrations/0004-identities.sql`, spec
   5/9): identity id, name, claimed platform subs (`tg:123`,
   `discord:abc`...). This is what `GET /v1/identities/{sub}`
   (`authd/http.go:371`) resolves for routd's `inspect_identity`.

## Control

| Affordance               | `/v1` verb                                                                     | Status     | Danger                                                                                                                                                                                      |
| ------------------------ | ------------------------------------------------------------------------------ | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| rotate key               | `POST /v1/keys/rotate` → `Authd.Rotate` (`authd/server.go:83`)                 | **to add** | no — old key keeps verifying inside its window                                                                                                                                              |
| revoke all keys          | `POST /v1/keys/revoke-all` → `RevokeAllNow` (`authd/server.go:106`)            | **to add** | **`.btn-danger`** — invalidates every live token instantly, type-to-confirm                                                                                                                 |
| issue token              | `POST /v1/tokens` issuer-mint (`authd/http.go:191`)                            | exists     | **`.btn-danger`** — credential grant; minted JWT shown once, never stored                                                                                                                   |
| revoke refresh family    | `DELETE /v1/refresh/families/{family}` → `revokeFamily` (`authd/store.go:203`) | **to add** | **`.btn-danger`** — logs the user out everywhere                                                                                                                                            |
| unlink provider identity | `DELETE /v1/users/{user_id}/identities/{provider}`                             | **to add** | **`.btn-danger`** when it is the user's last provider (lockout); refuse or double-confirm                                                                                                   |
| relink identity          | —                                                                              | —          | **non-goal**: linking is the user-driven OAuth flow (`/auth/<provider>?intent=link`, `authd/oauth.go:87`); an operator cannot prove a third party's provider login                          |
| enable/disable provider  | —                                                                              | —          | **non-goal**: providers are env-config mounted at boot (`registerOAuth`); no runtime toggle exists and the spec does not invent one — flipping a provider is a compose/env change + restart |

Issue-token form fields mirror the existing handler body (`typ`, `sub`,
`scope`, `folder`, `ttl_seconds`); the existing mode-selection +
ceiling rules apply unchanged (downscope vs issuer-mint,
`scope_exceeds_minter`, fail-closed on `grants_unavailable` —
`authd/http.go:224`).

## Required `/v1` work

`GET /v1/keys` is the **public JWKS** (`auth.PublicJWKS`,
`authd/http.go:106`) — public halves of serving keys only, no
created/retired metadata, and it must stay public-cacheable. The
dashboard therefore needs operator-gated reads that don't exist yet:

- `GET /v1/keys/meta` — all `signing_keys` rows, metadata only (kid,
  created_at, active, retired_at, serving_until, pub_pem). Excludes
  `priv_pem` at the SQL level, not by post-filtering.
- `POST /v1/keys/rotate` — wraps `Authd.Rotate`.
- `POST /v1/keys/revoke-all` — wraps `RevokeAllNow`.
- `GET /v1/refresh/families` — family summaries (family_id, sub, scope,
  issued_at, expires_at, state). No token hashes in the response.
- `DELETE /v1/refresh/families/{family}` — wraps `revokeFamily`.
- `GET /v1/users` (+ `GET /v1/users/{user_id}`) — `auth_users` +
  joined `oauth_identities`.
- `DELETE /v1/users/{user_id}/identities/{provider}` — unlink one
  provider row.
- `GET /v1/identities` — list `identities` + claim counts (the
  existing `GET /v1/identities/{sub}` is single-sub lookup only).

All new endpoints are bearer-gated like the existing surface
(`auth.VerifyHTTP` against `LocalKeySet`, `authd/server.go:143`) with
operator-tier scope; mutations emit `audit_log` rows
(`authd/migrations/0003-audit-log.sql`).

## Auth

Per `6/1`: proxyd `auth:"user"` transit on `/dash/authd/` +
`auth/dashauth.go` operator gate, CSRF same-origin on writes. Dash
handlers call the same store/`Authd` methods in-process; the `/v1`
faces above exist for dashd's cross-cutting pages (`6/2` ACL/profile
read authd over HTTP) and parity. No per-page exception — everything
here is operator-only.

## HTMX fragments

- `GET /dash/authd/x/keys` — key table rows (poll on the keys page).
- `GET /dash/authd/x/families?sub=` — refresh-family rows, filterable.
- `POST /dash/authd/x/rotate` / `POST /dash/authd/x/revoke-all` —
  return the refreshed key table; revoke-all requires the typed
  confirmation field.
- `POST /dash/authd/x/mint` — returns the one-time token panel.
- `GET /dash/authd/x/identity?sub=` — resolve one platform sub (same
  shape as `GET /v1/identities/{sub}`).

## Non-goals

Per `6/1`, plus: no JWT decoder/inspector for arbitrary pasted tokens;
no provider runtime toggles (above); no editing of `serviceGrants`
(declared in code, `authd/http.go:26` — a deploy concern); no grants
editing (grants belong to routd's ACL, surfaced in `6/2`); no
refresh-token issuance from the dashboard.

## Acceptance

- `/dash/authd/` route registered in `template/services/authd.toml`
  as a `[[proxyd_route]]`; hub tile appears and probes `GET /health`
  (db-ping handler, `authd/http.go:98`).
- Keys page lists every `signing_keys` row; no response body anywhere
  under `/dash/authd/` or the new `/v1` reads contains `priv_pem` or
  a raw/hashed refresh token (grep the rendered HTML in the e2e test).
- Rotate from the dashboard → JWKS at `GET /v1/keys` serves the new
  kid; old key still verifies an in-flight token until its window ends.
- Revoke-all → a token minted before the click fails verification
  immediately; the confirm gate blocks a bare POST.
- Revoking a refresh family → `POST /v1/refresh` with that family's
  live token returns 401 `invalid_refresh`.
- Unlinking a provider deletes exactly one `oauth_identities` row and
  is refused (or double-confirmed) for the last linked provider.
- Non-operator caller gets the theme 403 banner on every page.
