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
  still inside its serving window; persist keys to `auth.db`
- Mint access tokens: `MintForSubject` (scope ⊆ target's grants snapshot),
  `Downscope` (subset of parent), issuer-mint for a different sub
- Rotate keys (one active key enforced by partial-unique index);
  `RevokeAllNow` is emergency revoke
- Issue + rotate refresh tokens with reuse detection (replayed token
  revokes the whole family); re-snapshot grants on refresh
- Self-mint `service:authd` at boot and exchange daemon service keys for
  `service:<name>` tokens
- Resolve platform sender subs (`tg:123`, `discord:abc`) to canonical identity
  via `GET /v1/identities/{sub}` (reads `auth.db` identities/identity_claims)

## Tables owned

`signing_keys`, `refresh_tokens`, `auth_users`, `oauth_identities`,
`identities`, `identity_claims`, `identity_codes` — created by authd's
own migrations in `authd/migrations/`. authd owns `auth.db` and runs
its own migrations; it MUST NOT touch another daemon's DB (CLAUDE.md
DB-ownership rule).

## Entry points

- Binary: `authd/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface:
  - `GET /v1/keys` — public JWKS (`auth.PublicJWKS`)
  - `POST /v1/tokens` — mint / downscope
  - `POST /v1/service-token` — daemon service-key exchange
  - `POST /v1/refresh` — rotate refresh token
  - `GET /v1/identities/{sub}` — identity resolution (bearer-gated, `identity:read`)
  - `GET /auth/*` — OAuth login/callback/logout (mounted only when `AUTH_BASE_URL` set)
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (signing/verify primitives), `core`, `store`, `audit`, `obs`, `resreg`
- Direct SQLite via `modernc.org/sqlite` (owns `auth.db`)
- `routd` (optional, via `GRANTS_URL`) — authd fetches the login-time scope
  ceiling from routd at login and refresh; unset = empty-scope sessions.

## Configuration

- `DATABASE` / `DATA_DIR` — `auth.db` DSN (`DATA_DIR` resolves `<dir>/store/auth.db`
  alongside `routd.db`/`runed.db`/`onbod.db` so one `store/` chown covers all DBs)
- `AUTHD_SERVICE_KEY` — authd's bootstrap secret (self-mint `service:authd`)
- `AUTHD_SERVICE_KEYS` — `principal=secret,...` map for daemon exchange
- `GRANTS_URL` — routd grants endpoint (`http://routd:8080`); unset → empty-scope sessions
- `LISTEN_ADDR` — HTTP listen address (`:8080`)
- `ARIZUKO_INSTANCE` — instance name (observability + audit context)
- OAuth provider: `AUTH_BASE_URL`, `GITHUB_*`, `GOOGLE_*`, `DISCORD_*`, `TELEGRAM_*`

## Token lifetimes

- Access tokens: 15 minutes (hardcoded)
- Refresh tokens: 30 days (hardcoded)
- Retired keys verify for max access TTL (1 hour) after retirement

## Health signal

`GET /health` returns 200 when DB is reachable.

## Observability

slog → journald (always on). OTLP export when `OTEL_EXPORTER_OTLP_ENDPOINT` set
(spec `specs/5/O-otlp-export.md`). Audit events written to `auth.db` audit_log.

## Files

- `main.go` — boot, migrate, OAuth mount, signal handling
- `server.go` — `Authd` signer: key rotation, mint/downscope, refresh lineage
- `http.go` — `/v1/*` handlers + mux
- `store.go` — key + refresh-token persistence
- `oauth.go` — OAuth provider dance → ES256 mint
- `grants.go` — HTTP grants fetcher (login-time scope snapshot)

## Status

Live. Spec: `specs/5/1-auth-standalone.md`.
