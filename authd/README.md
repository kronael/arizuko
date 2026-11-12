# authd

authd signs platform tokens, publishes ES256 verification keys, and owns login identities and refresh-token families.

## Purpose

Separate daemon that owns the signing keypair and mints every platform
token. Backends never sign — they fetch authd's public JWKS and verify
locally via `auth.VerifyToken` (the `auth/` library). authd also acts as
the OAuth provider (GitHub/Google/Discord), exchanging a
successful login for an ES256 access + refresh token.

Backends verify tokens locally when their cached public keys contain the signing key.
An authd outage can block a new key fetch, login, refresh, or service-token exchange.
The signer owns key rotation and refresh-family revocation.

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
through the driver at boot. authd holds no SQL or DDL, and it
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
  - `GET /dash/authd/` and `POST /dash/authd/revoke` — authd's own operator dashboard: the signing-key
    lifecycle, the login families, and a per-login sign-out. Gated by the
    proxyd-stamped operator identity, not a bearer.

  The sole holder of `signing_keys:read`, `sessions:read` and `sessions:write`
  is `service:dashd`. Like `audit:read`,
  none is reachable by a human bearer, and `signing_keys`/`sessions`
  also refuse a folder-claimed caller outright: neither table has a
  folder column, so there is no predicate that could contain one.
  `serviceGrants` (`http.go`) is the whole per-principal scope map, and
  `service_dashd_test.go` bounds dashd's entry by name and by count.
  - `/auth/*` — configured OAuth login/callback/logout routes
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (signing/verify primitives), `core`, `store`, `audit`, `obs`, `resreg`
- `objstore` through `store.OwnerDriver` — the configured store endpoint, using the `authd.` table prefix
- `routd` (optional, via `GRANTS_URL`) — authd fetches the login-time scope
  ceiling from routd at login and refresh; unset = empty-scope sessions.

## Configuration

| Variable                                   | Default     | Meaning                                                                                                           |
| ------------------------------------------ | ----------- | ----------------------------------------------------------------------------------------------------------------- |
| `LISTEN_ADDR`                              | `:8080`     | HTTP listener.                                                                                                    |
| `AUTHD_SERVICE_KEY`                        | unset       | Self-mint bootstrap and exchange secret for service:authd.                                                        |
| `AUTHD_SERVICE_KEYS`                       | unset       | Comma-separated `service:<daemon>=<secret>` exchange map.                                                         |
| `GRANTS_URL`                               | unset       | routd base URL; unset produces empty-scope login sessions.                                                        |
| `AUTH_BASE_URL`                            | unset       | Required to mount OAuth routes; WEB_HOST alone does not mount them.                                               |
| `WEB_HOST`                                 | unset       | Fallback public origin.                                                                                           |
| `AUTH_SECRET`                              | unset       | Signs OAuth CSRF state.                                                                                           |
| `GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET`   | unset       | GitHub OAuth ID mounts the route; secret supports token exchange.                                                 |
| `GITHUB_ALLOWED_ORG`                       | unset       | Optional GitHub organization restriction.                                                                         |
| `GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET`   | unset       | Google OAuth ID mounts the route; secret supports token exchange.                                                 |
| `GOOGLE_ALLOWED_EMAILS`                    | unset       | Optional comma-separated email allowlist.                                                                         |
| `DISCORD_CLIENT_ID, DISCORD_CLIENT_SECRET` | unset       | Discord OAuth ID mounts the route; secret supports token exchange.                                                |
| `DEX_ISSUER, DEX_CLIENT_ID`                | unset       | Dex discovery URL and client; requires both to mount Dex login.                                                   |
| `DEX_CLIENT_SECRET`                        | unset       | Dex client credential when needed.                                                                                |
| `DYNAMODB_URL`                             | required    | DynamoDB-protocol store endpoint.                                                                                 |
| `AWS_REGION`                               | `us-east-1` | Store signing region.                                                                                             |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY` | required    | Daemon store identity; both values must be set.                                                                   |
| `STORE_CA`                                 | unset       | Base64 PEM endpoint certificate; unset uses system trust.                                                         |
| `ARIZUKO_INSTANCE`                         | unset       | Audit and observability instance label.                                                                           |
| `LOG_LEVEL`                                | `info`      | JSON stderr verbosity: debug, info, warn, error.                                                                  |
| `OTEL_EXPORTER_OTLP_ENDPOINT`              | unset       | Optional OTLP log export.                                                                                         |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`       | unset       | Optional OTLP trace export; independent of the log endpoint.                                                      |
| `MAX_CONCURRENT_CONTAINERS`                | absent      | Shared config rejects this variable whenever it is present; configure MAX_CONCURRENT_AGENTS for execution limits. |
| `METRICS_ENABLED`                          | `false`     | Literal `true` mounts public GET /metrics.                                                                        |

`core.LoadConfig` also parses the shared instance settings in [core/config.go](../core/config.go); authd uses the OAuth fields listed above.

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

  To select a new signing key, delete the `authd.signing_key_slot`
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
  are not round-tripped by `dump` / `apply`, because a key is
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

## Health signal

`GET /health` probes the store and returns 200 `ok` or 503 `store unreachable`.
The probe does not test the grants backend or an OAuth provider.

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

## Failures, timers, and coverage

```text
backends ──► authd /v1/keys ──► local JWT verification
browser through proxyd ──► authd OAuth ──► provider
                                   │
                                   ├──► routd:8080 grants snapshot
                                   └──► authd.* store entities
```

| Resource       | REST path                                             | MCP tool                                                |
| -------------- | ----------------------------------------------------- | ------------------------------------------------------- |
| `audit`        | `GET /v1/audit`                                       | No authd agent socket; catalog action is `query_audit`. |
| `signing_keys` | `GET /v1/signing_keys`                                | None.                                                   |
| `sessions`     | `GET /v1/sessions`, `DELETE /v1/sessions/{family_id}` | None.                                                   |

`StoreReach["authd"]` opens only `authd`. `audit_log` holds authd's mutations and daemon-start events.
Keys and refresh credentials persist in the remote store; authd owns no local key file or IPC socket.
The other daemons fetch JWKS or exchange their service key. proxyd forwards browser login and refresh calls here.

There is no scheduled key rotation or refresh-token cleanup goroutine. Expiry checks happen on requests and key load.
The grants HTTP client has a 10 s timeout; the Dex discovery/token client has a 15 s timeout.
Shutdown allows 5 s. A grants 5xx fails login or refresh rather than treating the person as ungranted.
A store setup or initial signer failure exits. A running signer keeps its key in memory; store-dependent operations can still fail.
Cached verifier keys can serve existing tokens during an authd outage, within token and key validity.
Grep `open authd's store`, `prepare storage`, `init signer`, `mint service token`,
`oauth /auth/* not mounted`, and `refresh-token family NOT revoked`.
`/metrics` includes HTTP request metrics and token mint/refresh counters when enabled.

Tests cover signer, refresh lineage, identity linking, service-token exchange, and resource gates with stubbed providers.
Local documentation checks do not verify live provider redirects or revocation propagation through every verifier cache.

Sources: [boot](main.go), [mux and service grants](http.go), [signer](server.go),
[OAuth](oauth.go), [Dex](dex.go), [store entities](authstore/authstore.go), [store reach](../store/owner_reach.go).

## OAuth route patterns

| Method and full path                                   | Mount condition                                    |
| ------------------------------------------------------ | -------------------------------------------------- |
| `GET /auth/login`, `POST /auth/logout`, `GET /auth/me` | AUTH_BASE_URL is set and core configuration loads. |
| `GET /auth/github`, `GET /auth/github/callback`        | GITHUB_CLIENT_ID is set.                           |
| `GET /auth/google`, `GET /auth/google/callback`        | GOOGLE_CLIENT_ID is set.                           |
| `GET /auth/discord`, `GET /auth/discord/callback`      | DISCORD_CLIENT_ID is set.                          |
| `GET /auth/dex`, `GET /auth/dex/callback`              | DEX_ISSUER and DEX_CLIENT_ID are set.              |

Each provider row also requires the AUTH_BASE_URL mount condition.
