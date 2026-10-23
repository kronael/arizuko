# authd

ES256 token authority: the sole platform-token signer + JWKS publisher.

## Purpose

Separate daemon that owns the signing keypair and mints every platform
token. Backends never sign — they fetch authd's public JWKS and verify
locally via `auth.VerifyToken` (the `auth/` library). authd also acts as
the OAuth provider (GitHub/Google/Discord), exchanging a
successful login for an ES256 access + refresh token.

One signer and offline verifiers is the whole design. Verification is a
pure function with no per-request hop, so an authd outage does not stop
tokens already issued, and the one signer is the one place to rotate or
revoke a key.

## Responsibilities

- Hold the active ES256 (P-256) signing key in memory plus every key
  still inside its serving window; persist them to its own store
- Mint access tokens: `MintForSubject` (scope ⊆ target's grants snapshot),
  `Downscope` (subset of parent), issuer-mint for a different sub
- Rotate keys. Exactly one active key is enforced by the `signing_key_slot`
  entity holding ONE item, so two signers are unrepresentable rather than
  merely refused
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
through the driver at boot. authd runs no statement and holds no DDL, and it
reaches its own owner and no other daemon's. Every entity takes one partition
for the whole daemon, so every non-point read is a whole-partition read.

| Entity             | Coordinate                  | Attributes                                                                   |
| ------------------ | --------------------------- | ---------------------------------------------------------------------------- |
| `signing_keys`     | the kid                     | the ES256 key pair, created and retired instants                             |
| `signing_key_slot` | one item                    | the kid that signs now                                                       |
| `auth_users`       | the sub                     | name, created instant                                                        |
| `oauth_identities` | `<provider>:<provider sub>` | the account that login belongs to, linked instant                            |
| `refresh_tokens`   | the token hash              | family, sub, scope, audience, issued, expiry, used and revoked instants      |
| `refresh_families` | the family id               | sub, scope, audience, `rotations`, renewed and expiry instants, `revoked_at` |

Two of the six carry an invariant a column could not:

- **`signing_key_slot`** holds ONE item naming the kid that signs. "Exactly one
  active key" is a slot rather than a partial-unique index, which no key-value
  engine has; as a slot, two signers are unrepresentable.
- **`refresh_families`** holds one item per login, carrying `revoked_at` plus
  the rotation count and start instant a session listing reports. Killing a
  family is one guarded write instead of an UPDATE across every row the login
  ever rotated through.
  Rotation writes the successor token and the family's new `rotations` count
  in the same batch as the claim, under a condition on `revoked_at` absent and
  on the count it read. A revoke that lands first refuses the whole batch, so
  no successor is written and then orphaned. The batch's committed answer,
  never a count of touched rows, says whether a rotation won.

## Entry points

- Binary: `authd/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface:
  - `GET /v1/keys` — public JWKS (`auth.PublicJWKS`)
  - `POST /v1/tokens` — mint / downscope
  - `POST /v1/service-token` — daemon service-key exchange
  - `POST /v1/refresh` — rotate refresh token
  - `GET /v1/identities/{sub}` — identity resolution (bearer-gated, `identity:read`)
  - `GET /v1/audit` — authd's own `audit_log` (bearer-gated, `audit:read`). Operator-only in effect: `audit:read` is a resource:verb scope
    and a user token carries folder globs, so no human bearer can satisfy it.
    `service:dashd` is the sole holder and `/dash/audit/` is operator-gated.
  - `GET /v1/signing_keys` — signing-key METADATA (bearer-gated,
    `signing_keys:read`). kid, algorithm, `active`, `created_at`,
    `retired_at` and the derived `serves_until` — the lifecycle the JWK Set
    drops. Never key material: `SigningKeyMetadata` projects exactly
    `created_at` and `retired_at`, so the private half is not in the answer at
    all.
  - `GET /v1/sessions` — refresh-token FAMILIES (bearer-gated, `sessions:read`).
    One row per login lineage; `token_hash` is never selected.
  - `DELETE /v1/sessions/{family_id}` — operator revoke of one family
    (bearer-gated, `sessions:write`). The incident-response verb;
    `POST /auth/logout` remains the self-service one. Writes its `audit_log`
    row inside the revoke's own transaction.
  - `GET|POST /dash/authd/` — authd's own operator dashboard: the signing-key
    lifecycle, the login families, and a per-login sign-out. Gated by the
    proxyd-stamped operator identity, not a bearer.

  The sole holder of `signing_keys:read`, `sessions:read` and `sessions:write`
  is `service:dashd`. Like `audit:read`,
  none is reachable by a human bearer, and `signing_keys`/`sessions`
  also refuse a folder-claimed caller outright: neither table has a
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

- `DYNAMODB_URL` + `AWS_*` + `STORE_CA` — the store server authd dials for its
  own table prefix. Without them authd refuses to start
- `AUTHD_SERVICE_KEY` — authd's bootstrap secret (self-mint `service:authd`)
- `AUTHD_SERVICE_KEYS` — `principal=secret,...` map for daemon exchange
- `GRANTS_URL` — routd grants endpoint (`http://routd:8080`); unset → empty-scope sessions
- `LISTEN_ADDR` — HTTP listen address (`:8080`)
- `ARIZUKO_INSTANCE` — instance name (observability + audit context)
- OAuth provider: `AUTH_BASE_URL`, `GITHUB_*`, `GOOGLE_*`, `DISCORD_*`
- `DEX_ISSUER` + `DEX_CLIENT_ID` (+ `DEX_CLIENT_SECRET`) — mount `/auth/dex`:
  Dex authenticates, authd maps the ID token's `federated_claims` to
  `<connector_id>:<user_id>` and mints the session as for a native provider
  Unset → unmounted.

## Token lifetimes

- Access tokens: 15 minutes (`accessTTL`, hardcoded)
- Refresh tokens: 30 days (`refreshTTL`, hardcoded)
- A mint clamps any TTL to `maxAccessTTL`, one hour, and a retired key keeps
  verifying for that long after retirement

## Keys, mints and logins

- **Keys.** A kid is `<created-unix>-<8 hex>`, and the first boot generates
  a key. Validity is time-based: a key verifies while it is active or until
  `retired_at + maxAccessTTL`. There is no revoked flag and no revocation
  list, because verifiers stay offline. A normal revoke waits out the access
  TTL; a refresh revoke marks the family as revoked.

  To invalidate existing access tokens, delete the `authd.signing_key_slot`
  item and restart authd. authd creates a new signing key. Recently retired
  keys still verify tokens until `retired_at + maxAccessTTL`, up to one hour.
  Offline verifiers can accept existing access tokens with cached keys until
  the tokens expire.
  Key rotation does not revoke refresh tokens.

  To sign everyone out, stop authd before you change its store. Connect to
  `DYNAMODB_URL` with the authd credentials in `env/authd.env` and the
  certificate in `STORE_CA`. See [the store setup in INSTALL.md](../INSTALL.md#the-per-daemon-store-keys).
  Use DynamoDB `DeleteTable` to delete `authd.refresh_families` and
  `authd.refresh_tokens`. Wait for both deletions to finish. Use DynamoDB
  `TransactWriteItems` to delete the `authd.signing_key_slot` item at
  `part="authd", sort="active"`. Restart authd to create empty refresh tables
  and a new signing key. Existing access tokens can still work until their
  expiry.

- **`POST /v1/tokens` has two modes.** Issuer-mint needs `tokens:mint` and a
  different target sub; the granted scope is bounded by the TARGET's grants,
  because a login or invite flow mints for an account whose grants the
  minter does not hold (`403 scope_exceeds_minter`). Every other request is
  a downscope: same sub, scope a subset of the caller's, folder inside the
  caller's subtree, TTL clamped to the parent's remaining life
  (`403 scope_exceeds_parent`). Global `*:*` is refused in both.
- **Account linking.** `(provider, provider_sub)` belongs to one account.
  Linking an identity another account owns is a hard fail; accounts are
  never merged, and there is no auto-link by verified email, because each of
  those is an account-takeover path. The canonical sub is the first login's
  `<provider>:<sub>` and never moves, because `acl` rows key on it. There is
  no unlink. The login a person presented survives only in authd's
  `audit_log` (`login`), never as a claim, because a claim would vanish on
  the first refresh.
- **The login snapshot.** authd reads the scope ceiling from routd
  (`GRANTS_URL`) at login and refresh. `404 no_grants` gives an empty-scope
  session that lands on `/onboard`. A 5xx fails the login with `503`, so an
  outage never looks like an empty session. `GRANTS_URL` unset makes every
  session empty-scope.
- **Service bootstrap.** A daemon presents its own `AUTHD_SERVICE_KEY`,
  and authd compares it in constant time against every entry of
  `AUTHD_SERVICE_KEYS` (`principal=secret,...`). A
  leaked key buys one daemon's scoped token (`serviceGrants`); rotating it
  is regenerate and restart. Adapters exchange as the DAEMON principal
  (`service:teled`), never as `CHANNEL_NAME`.
- **The operator reads** `signing_keys` and `sessions` have no MCP face and
  are not round-tripped by `export` / `apply`, because a key is
  instance-global and a session is keyed by sub: no folder could contain
  either.
- **Hardening.** Cookies carry `Secure` when `AUTH_BASE_URL` is HTTPS. The
  refresh cookie is `HttpOnly`, `Path=/`, `SameSite=Lax`. Every provider
  redirect carries a signed CSRF state and PKCE. Google takes an optional
  `GOOGLE_ALLOWED_EMAILS`, GitHub an optional `GITHUB_ALLOWED_ORG`. All
  providers converge on one `issueSession` path (`oauth.go`).

## Dex is an upstream broker, not a replacement

`DEX_ISSUER` and `DEX_CLIENT_ID` mount `/auth/dex` beside the native
providers; unset leaves authd unchanged. authd asks for the scope `openid
profile federated:id` (`dexScope`, `authd/dex.go`), verifies the ID token
and maps its `federated_claims` to `<connector_id>:<user_id>`. The
connector id in Dex's configuration is the principal namespace, and authd
keeps no allowlist of connectors. A login with no `federated_claims` fails.
Everything after the mapping is the native path: alias resolution, the
scope snapshot, the `arz/folder` claim and the ES256 mint.

authd keeps minting because arizuko's tokens carry claims an IdP cannot
issue. `arz/folder` and the login-time scope snapshot come from routd's
grants. Without them in the token, every backend would look up the folder
and the grants per request. `POST /v1/service-token` is arizuko's too:
`auth.ProxydTransit` pins `typ=service` and `sub=service:proxyd` on
arizuko's own verifier.

Operator facts for a Dex deploy, per Dex v2.45.1 as measured on 2026-09-03
and not measured again since:

- Dex's ID token has a closed claim set: `email`, `groups`, `name`,
  `preferred_username`, `federated_claims`. It has no hook for a per-user
  claim.
- Dex v2.45.1 has no `client_credentials` grant.
- Dex emits `federated_claims` only for the `federated:id` scope.
- The `github` connector reports the numeric GitHub id unless its config
  sets `useLoginAsID: true`. An `acl` row keyed on the login, such as
  `github:kronael`, stops matching without it.
- Discord and Facebook ride the generic `oauth` connector. Twitter/X cannot:
  the connector sends no PKCE, and X nests the user id under `data`.

## Health signal

`GET /health` returns 200 when the STORE answers — the endpoint or the file,
whichever this instance is on.

## Observability

slog → journald (always on). OTLP export when `OTEL_EXPORTER_OTLP_ENDPOINT` set
(`obs/README.md`). Audit events land in authd's own
`audit_log` entity (`daemon.start`, `login`, and `sessions:delete` for an
operator revoke) and are served read-only at `GET /v1/audit`, which
`/dash/audit/` federates. `params_summary` has exactly one writer,
`daemon.start`, and its DSN field is redacted at the writer
(`audit.redactRE`).

## Files

- `main.go` — boot, OAuth mount, signal handling
- `storage.go` — the driver choice and the entity creation it applies
- `server.go` — `Authd` signer: key rotation, mint/downscope, refresh lineage
- `http.go` — `/v1/*` handlers + mux
- `authstore/` — the storage layer, and it holds no SQL: every method is
  `objstore.Query` or `objstore.GuardedBatch`. One file per entity
  (`signing_keys.go`, `refresh_tokens.go`, `auth_users.go`, which also carries
  `oauth_identities` because no operation touches one without the other), plus
  `import.go` for the one-way move. A second driver changes nothing here.
- `oauth.go` — OAuth provider dance → ES256 mint
- `audit_resource.go` — the `GET /v1/audit` resreg mount, plus `scopeGate` /
  `instanceWideGate`, the two gate builders every authd resource injects
- `signing_keys_resource.go` — `GET /v1/signing_keys`; the constant SELECT list
  in `authstore.SigningKeyMetadata` is what keeps key material off the wire
- `sessions_resource.go` — `GET /v1/sessions` + the operator revoke
- `grants.go` — HTTP grants fetcher (login-time scope snapshot)
- `dash.go` — `/dash/authd/` and its revoke form

## Coverage

`go test ./authd/ -short -cover` reports 69.9% (2026-09-13). Two shapes are
uncovered and both need a real remote:

- `main` and `handleHealth` — boot and the liveness probe. `make test-e2e`'s.
- `googleCallback`, `githubCallback`, `discordCallback`, `logout` —
  the provider redirect halves, which need the provider to answer. What they
  converge on IS covered: `issueSession` 83.7%, `dispatch` 64.3%,
  `authorizeURL` 71.4%. Untested is the per-provider parameter unpacking in
  front of the mint, not the ES256 path.

Key rotation, the refresh lineage, service-token exchange, the three resreg
resources and `authstore` are covered.
