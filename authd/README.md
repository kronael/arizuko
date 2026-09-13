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
- Resolve platform sender subs (`tg:123`, `discord:abc`) to the canonical user
  via `GET /v1/identities/{sub}` (reads `auth_users` + `oauth_identities`, the
  model the OAuth login path populates)

## Entities owned

`signing_keys`, `signing_key_slot`, `refresh_tokens`, `refresh_families`,
`auth_users`, `oauth_identities` — declared by `authstore.Entities` and created
through the driver at boot. authd runs no statement and holds no DDL (spec
`specs/6/37-store-model.md` step 5); it owns `auth.db` and MUST NOT touch
another daemon's DB (CLAUDE.md DB-ownership rule).

Two of the six have no table behind them in the old schema, and each carries an
invariant a column could not:

- **`signing_key_slot`** holds ONE item naming the kid that signs. "Exactly one
  active key" used to be a partial-unique index, which no key-value engine has;
  as a slot, two signers are unrepresentable.
- **`refresh_families`** holds one item per login, carrying `revoked_at` plus
  the rotation count and start instant a session listing reports. Killing a
  family is one guarded write instead of an UPDATE across every row the login
  ever rotated through.

## Entry points

- Binary: `authd/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface:
  - `GET /v1/keys` — public JWKS (`auth.PublicJWKS`)
  - `POST /v1/tokens` — mint / downscope
  - `POST /v1/service-token` — daemon service-key exchange
  - `POST /v1/refresh` — rotate refresh token
  - `GET /v1/identities/{sub}` — identity resolution (bearer-gated, `identity:read`)
  - `GET /v1/audit` — authd's own `audit_log` (bearer-gated, `audit:read`;
    spec `5/I`). Operator-only in effect: `audit:read` is a resource:verb scope
    and a user token carries folder globs, so no human bearer can satisfy it.
    `service:dashd` is the sole holder and `/dash/audit/` is operator-gated.
  - `GET /v1/signing_keys` — signing-key METADATA (bearer-gated,
    `signing_keys:read`; spec `5/1`). kid, algorithm, `active`, `created_at`,
    `retired_at` and the derived `serves_until` — the lifecycle the JWK Set
    drops. Never key material: `priv_pem`/`pub_pem` appear in no SELECT list.
  - `GET /v1/sessions` — refresh-token FAMILIES (bearer-gated, `sessions:read`).
    One row per login lineage; `token_hash` is never selected.
  - `DELETE /v1/sessions/{family_id}` — operator revoke of one family
    (bearer-gated, `sessions:write`). The incident-response verb;
    `POST /auth/logout` remains the self-service one. Writes its `audit_log`
    row inside the revoke's own transaction.

  The sole holder of `signing_keys:read`, `sessions:read` and `sessions:write`
  is `service:dashd`, which spends them on `/dash/authd/` — the operator
  cockpit these three exist to feed (`dashd/authd_page.go`). Like `audit:read`,
  none is reachable by a human bearer, and `signing_keys`/`sessions`
  additionally refuse a folder-claimed caller outright: neither table has a
  folder column, so there is no predicate that could contain one.
  `serviceGrants` (`http.go`) is the whole per-principal scope map, and
  `service_dashd_test.go` bounds dashd's entry by name and by count.
  - `GET /auth/*` — OAuth login/callback/logout (mounted only when `AUTH_BASE_URL` set)
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (signing/verify primitives), `core`, `store`, `audit`, `obs`, `resreg`
- `objstore` through `store.OwnerDriver` — the endpoint the instance is
  served from, or authd's own file
- `routd` (optional, via `GRANTS_URL`) — authd fetches the login-time scope
  ceiling from routd at login and refresh; unset = empty-scope sessions.

## Configuration

- `DATABASE` / `DATA_DIR` — the file authd opens when the instance keeps one
  (`DATA_DIR` resolves `<dir>/store/authd/auth.db`, beside the other owners so
  one `store/` chown covers them). Ignored once `DYNAMODB_URL` names an endpoint
- `AUTHD_SERVICE_KEY` — authd's bootstrap secret (self-mint `service:authd`)
- `AUTHD_SERVICE_KEYS` — `principal=secret,...` map for daemon exchange
- `GRANTS_URL` — routd grants endpoint (`http://routd:8080`); unset → empty-scope sessions
- `LISTEN_ADDR` — HTTP listen address (`:8080`)
- `ARIZUKO_INSTANCE` — instance name (observability + audit context)
- OAuth provider: `AUTH_BASE_URL`, `GITHUB_*`, `GOOGLE_*`, `DISCORD_*`, `TELEGRAM_*`
- `DEX_ISSUER` + `DEX_CLIENT_ID` (+ `DEX_CLIENT_SECRET`) — mount `/auth/dex`:
  Dex authenticates, authd maps the ID token's `federated_claims` to
  `<connector_id>:<user_id>` and mints the session as for a native provider
  (spec `6/22` step 4). Unset → unmounted.

## Token lifetimes

- Access tokens: 15 minutes (hardcoded)
- Refresh tokens: 30 days (hardcoded)
- Retired keys verify for max access TTL (1 hour) after retirement

## Health signal

`GET /health` returns 200 when DB is reachable.

## Observability

slog → journald (always on). OTLP export when `OTEL_EXPORTER_OTLP_ENDPOINT` set
(spec `specs/5/O-observability.md`). Audit events recorded in `auth.db`'s audit_log entity (`daemon.start`, `login`, and
`sessions:delete` for an operator revoke) and served read-only at
`GET /v1/audit`, which `/dash/audit/` federates. `params_summary` has exactly one writer,
`daemon.start`; its DSN field is redacted at the writer (`audit.redactRE`) and
scrubbed from history by the migration that ran on every live instance before
authd stopped carrying one.

## Files

- `main.go` — boot, OAuth mount, signal handling
- `storage.go` — the driver choice and the entity creation it applies
- `server.go` — `Authd` signer: key rotation, mint/downscope, refresh lineage
- `http.go` — `/v1/*` handlers + mux
- `authstore/` — the storage layer, and it holds no SQL: every method is
  `objstore.Query` or `objstore.GuardedBatch`. One file per entity
  (`signing_keys.go`, `refresh_tokens.go`, `auth_users.go`, which also carries
  `oauth_identities` because no operation touches one without the other), plus
  `import.go` for the one-way move. A second driver changes nothing here
  (spec `specs/6/37-store-model.md` step 5, enforced by
  `db_utils/sql_layer_test.go`).
- `oauth.go` — OAuth provider dance → ES256 mint
- `audit_resource.go` — the `GET /v1/audit` resreg mount, plus `scopeGate` /
  `instanceWideGate`, the two gate builders every authd resource injects
- `signing_keys_resource.go` — `GET /v1/signing_keys`; the constant SELECT list
  in `authstore.SigningKeyMetadata` is what keeps key material off the wire
- `sessions_resource.go` — `GET /v1/sessions` + the operator revoke
- `grants.go` — HTTP grants fetcher (login-time scope snapshot)

## Status

Live. Spec: `specs/5/1-auth-standalone.md`.
