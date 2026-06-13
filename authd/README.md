# authd

ES256 token authority: the sole platform-token signer + JWKS publisher.

## Purpose

Separate daemon that owns the signing keypair and mints every platform
token. Backends never sign — they fetch authd's public JWKS and verify
locally via `auth.VerifyToken` (the `auth/` library). authd also acts as
the OAuth provider (GitHub/Google/Discord/Telegram widget), exchanging a
successful login for an ES256 access + refresh token. Spec: `specs/5/1`.

## Responsibilities

- Hold the active ES256 (P-256) signing key in memory plus every key
  still inside its serving window; persist keys to `auth.db`.
- Mint access tokens: `MintForSubject` (scope bounded by the target's
  grants snapshot), `MintNarrower`/`Downscope` (subset of parent), and
  issuer-mint for a different sub.
- Rotate keys (one active key enforced by a partial-unique index);
  `RevokeAllNow` is the emergency revoke.
- Issue + rotate refresh tokens with reuse detection (a replayed token
  revokes the whole family); re-snapshot grants on refresh.
- Self-mint `service:authd` at boot and exchange daemon service keys for
  `service:<name>` tokens.

## Tables owned

`signing_keys`, `refresh_tokens`, `auth_users`, `oauth_identities` —
created by authd's own migrations in `authd/migrations/`. authd owns
`auth.db` and runs its own migrations; it MUST NOT touch another
daemon's DB — e.g. routd's `routd.db` (CLAUDE.md DB-ownership rule).

## Entry points

- Binary: `authd/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface:
  - `GET /v1/keys` — public JWKS (`auth.PublicJWKS`)
  - `POST /v1/tokens` — mint / downscope
  - `POST /v1/service-token` — daemon service-key exchange
  - `POST /v1/refresh` — rotate refresh token
  - `GET /auth/*` — OAuth provider routes (mounted only when provider config present)
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (signing/verify primitives), `core`, `store`, `audit`, `obs`, `resreg`
- Direct SQLite via `modernc.org/sqlite` (owns `auth.db`)
- `routd` (optional, via `GRANTS_URL`) — authd is not the grants
  authority; it fetches the login-time scope ceiling from routd (the
  ACL owner).

## Configuration

- `DATABASE` / `DATA_DIR` — `auth.db` DSN (DATA_DIR resolves `<dir>/store/auth.db`,
  alongside routd.db/runed.db/onbod.db so one `store/` chown covers all DBs)
- `AUTHD_SERVICE_KEY` — authd's own bootstrap secret (self-mint identity)
- `AUTHD_SERVICE_KEYS` — `principal=secret,...` map for daemon exchange
- `GRANTS_URL` — routd grants fetcher (`http://routd:8080`); unset → sessions are empty-scope
- `LISTEN_ADDR`, OAuth provider vars (`GITHUB_*`, `GOOGLE_*`, `DISCORD_*`, `AUTH_BASE_URL`)

## Health signal

`GET /health` returns 200 once the signer is live with at least one
serving key.

## Files

- `main.go` — boot, migrate, OAuth mount, signal handling
- `server.go` — `Authd` signer: key rotation, mint/downscope, refresh lineage
- `http.go` — `/v1/*` handlers + mux
- `store.go` — key + refresh-token persistence
- `oauth.go` — OAuth provider dance → ES256 mint
- `grants.go` — HTTP grants fetcher (login-time scope snapshot)

## Status

Live — the split is the only topology (gated removed, v0.50.0). Spec: `specs/5/1`.
