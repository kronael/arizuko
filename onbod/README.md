# onbod

Onboarding daemon: gated admission queue + OAuth link.

## Purpose

Self-service onboarding. New JIDs receive a one-time link, OAuth
(GitHub/Google/Discord/Telegram widget) confirms identity, onbod creates
a user world via `container.SetupGroup`. Optional per-gate daily limits
throttle admission.

## Responsibilities

- Poll `awaiting_message` rows; send auth link via routd's outbound API.
- Serve `/onboard` landing, OAuth callbacks, username picker, world-creation page.
- Match users to `ONBOARDING_GATES` (github-org, google-domain, catch-all); enforce per-gate daily limits.
- Promote queued users to `approved` via `admitFromQueue` loop (~60s).
- Second-JID auto-link when a user already has a world.

## Tables owned

`onboarding`, `invites`, `onboarding_gates` (+ its own `audit_log`). In
the SPLIT topology onbod OWNS these in `onbod.db` (`ONBOD_DB_PATH`,
migrations under `onbod/migrations/`); in the monolith they stay in the
shared `messages.db` and `ONBOD_DB_PATH` is unset — `obdb` aliases the
messages.db handle. The writers reach the owned tables through onbod's
`/v1/*` admin surface (routd), or write `onbod.db` directly with the same
FS-access discipline (host CLI `arizuko invite`/`gate`, FS-mounted dashd).

`auth_users` is NOT onbod-owned in the split — it stays in messages.db
(authd territory); onbod's dashboard cross-reads it. The acl-grant half
of invite REDEMPTION (`ConsumeInvite` → `acl` row) and the dashboard's
`acl`/`groups`/`routes` cross-reads still target messages.db in the
split; federating those to routd/authd is follow-up, out of this pass.

## Entry points

- Binary: `onbod/main.go`; admin surface: `onbod/admin.go`; owned-DB
  open: `onbod/db.go`
- Listen: `$ONBOD_LISTEN_ADDR` (default `:8080`); mounts `GET /openapi.json`
  (`resreg.OpenAPIHandler("onbod", ["onboarding_gates"])` — only
  `onboarding_gates` is a resreg resource; the invite endpoints below are
  hand-mounted, so they don't appear in `/openapi.json`). Bearer-gated
  admin endpoints (verified against authd JWKS; nil keyset = open):
  `POST /v1/invites`, `GET /v1/invites`, `DELETE /v1/invites/{token}`,
  `GET /v1/gates`, `PUT /v1/gates/{gate}`, `DELETE /v1/gates/{gate}`
- Disable: `ONBOARDING_ENABLED=0` (exits immediately)

## Dependencies

- `auth` (OAuth, JWT, identity), `chanlib` (env helpers, router client), `container` (SetupGroup), `core`, `store`, `theme`

## Configuration

- `ONBOARDING_ENABLED`, `ONBOARDING_GATES`, `ONBOARDING_GREETING`, `ONBOARDING_PROTOTYPE`
- `ROUTER_URL`, `AUTHD_URL`, `AUTHD_SERVICE_KEY`, `AUTH_SECRET`, `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GOOGLE_CLIENT_ID/SECRET`, `DISCORD_CLIENT_ID/SECRET`
- `GITHUB_ALLOWED_ORG`, `GOOGLE_ALLOWED_EMAILS`

## Health signal

`GET /health` returns 200 when DB is reachable. Queued users see their
position on `/onboard` (auto-refreshes every 30s).

## `/v1/*` surface

Per `specs/5/5-uniform-mcp-rest.md`:

- `/v1/invites` — list/create/revoke invites (SHIPPED). routd's `/invite`
  command federates here; the host CLI `arizuko invite` and dashd write
  `onbod.db` directly (FS-mounted).
- `/v1/gates` — list/upsert/delete onboarding gates (SHIPPED). routd's
  `/gate` command federates here; CLI `arizuko gate` + dashd write direct.
- `/v1/users` — read user identity / lookup by `sub` (PLANNED); today
  dashd reads `auth_users` directly. `dashd /dash/profile/` migrates to
  `GET onbod/v1/users/{sub}` once `auth_users` ownership is settled.

REST verbs match the platform-api shape (`GET`, `POST`, `PUT`, `DELETE`).

## Token role

onbod uses the same `auth/` library every other daemon does — it is
**not** a special-case auth path. authd is the sole platform-token
signer; onbod never holds a signing key.

- **Verifier (today).** onbod fetches authd's JWKS at boot
  (`auth.FetchKeys` → `KeySet`) and gates its public routes with the local
  `stripUnsignedGuard(PROXYD_HMAC_SECRET, ks)` — it keeps the
  proxyd-stamped `X-User-Sub` (the OAuth'd end-user) only when the request
  proves it transited proxyd, via a legacy HMAC `X-User-Sig` OR a valid
  ES256 bearer (proxyd's `service:proxyd` transit token). The bearer is a
  transit proof ONLY; its `service:proxyd` subject never overwrites the
  end-user `X-User-Sub`. `/v1/invites` and
  `/v1/users` (planned) will additionally check `users:read` /
  `invites:write` scope via `auth.HasScope` — identical pattern to timed
  and routd.
- **Session bootstrap (today).** OAuth callback runs the shared
  `createOAuthSession` path and sets the onboarding cookies; it does not
  mint platform ES256 tokens. A brand-new user's first authoritative
  token comes from authd once they sign in via proxyd
  (`MintForSubject`), bounded by their grants snapshot.

## Cross-daemon flow

```
new JID → onbod /onboard → OAuth callback → user record + session cookie
       → user signs in via proxyd → authd MintForSubject(user:abc, grants)
       → user hits dashd → dashd verifies token via auth.VerifyHTTP(r, ks)
       → dashd renders, fetching identity via GET onbod/v1/users/{sub}
              Authorization: Bearer <user-token>
```

onbod imports `auth/` like every other daemon; verification middleware
is shared, not bespoke.

## Testing

Integration tests set `DATA_DIR` to a temp dir; unit tests omit it and
fall back to cwd. `handleCreateWorld` handles both: when `cfg.core` is nil
(test env without a full config), it calls `core.LoadConfig()` from env vars
rather than panicking. Do not remove this nil-check as dead code.

## Files

- `main.go` — config, poll loop, HTTP handlers, OAuth wiring
- `integration_test.go` — end-to-end flow tests

## Related docs

- `specs/4/26-prototypes.md` (prototype mechanic)
- `specs/4/9-acl-unified.md` (canonical ACL)
- `specs/5/5-uniform-mcp-rest.md` (full `/v1/*` contract, token model)
- `ARCHITECTURE.md` (Onboarding section)
