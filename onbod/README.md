# onbod

Onboarding daemon: gated admission queue + OAuth link.

## Purpose

Self-service onboarding. New JIDs receive a one-time link, OAuth
(GitHub/Google/Discord/Telegram widget) confirms identity, onbod creates
a user world via `container.SetupGroup`. Optional per-gate daily limits
throttle admission.

## Responsibilities

- Poll `awaiting_message` rows; send auth link via gated's outbound API.
- Serve `/onboard` landing, OAuth callbacks, username picker, world-creation page.
- Match users to `ONBOARDING_GATES` (github-org, google-domain, catch-all); enforce per-gate daily limits.
- Promote queued users to `approved` via `admitFromQueue` loop (~60s).
- Second-JID auto-link when a user already has a world.

## Tables owned

`invites`, `admissions`, `auth_users`. gated runs the migrations, but
onbod is the only writer. Other daemons read user identity via onbod's
API rather than touching `auth_users` directly.

## Entry points

- Binary: `onbod/main.go`
- Listen: `$ONBOD_LISTEN_ADDR` (default `:8080`; `/v1/invites`, `/v1/users` planned)
- Disable: `ONBOARDING_ENABLED=0` (exits immediately)

## Dependencies

- `auth` (OAuth, JWT, identity), `chanlib` (env helpers, router client), `container` (SetupGroup), `core`, `store`, `theme`

## Configuration

- `ONBOARDING_ENABLED`, `ONBOARDING_GATES`, `ONBOARDING_GREETING`, `ONBOARDING_PROTOTYPE`
- `ROUTER_URL`, `CHANNEL_SECRET`, `AUTH_SECRET`, `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GOOGLE_CLIENT_ID/SECRET`, `DISCORD_CLIENT_ID/SECRET`
- `GITHUB_ALLOWED_ORG`, `GOOGLE_ALLOWED_EMAILS`

## Health signal

`GET /health` returns 200 when DB is reachable. Queued users see their
position on `/onboard` (auto-refreshes every 30s).

## Planned `/v1/*` surface

Per `specs/6/R-platform-api.md`, onbod will serve two namespaces:

- `/v1/invites` — list/create/revoke invites; today only `arizuko invite`
  CLI mutates these
- `/v1/users` — read user identity / lookup by `sub`; today read directly
  from `auth_users` by dashd

REST verbs match the platform-api shape (`GET`, `POST`, `PATCH`,
`DELETE`). `dashd /dash/profile/` migrates from `auth_users` direct
read to `GET onbod/v1/users/{sub}`.

## Token role

onbod is both a verifier and an issuer, but uses the same `auth/`
library every other daemon does — it is **not** a special-case auth
path:

- **Verifier.** `/v1/invites` and `/v1/users` validate incoming JWTs
  via `auth.VerifyHTTP`, check `users:read` / `invites:write` scope,
  and folder match — identical pattern to timed and gated.
- **Issuer.** At invite redemption, onbod calls `auth.Mint(...)` to
  produce an initial user-session JWT. Same HS256 / `AUTH_SECRET` /
  claim shape as proxyd's session tokens. Narrower scopes — typically
  just `users:read` for the user's own `sub`, time-limited (short TTL)
  — and `iss: "onbod"` so audit can distinguish.

The redemption token is what bootstraps a brand-new user before they
have a proxyd OAuth session; once they sign in via proxyd they get the
broader session token from there.

## Cross-daemon flow

```
new JID → onbod /onboard → OAuth callback → user record created
       → onbod auth.Mint(sub=user:abc, scope=[users:read self], iss=onbod)
       → token handed back via redemption page / set-cookie
       → user hits dashd → dashd verifies token via auth.VerifyHTTP
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
- `specs/5/A-auth-consolidated.md` (supersedes mass-onboarding + ACL specs)
- `specs/6/R-platform-api.md` (full `/v1/*` contract, token model)
- `ARCHITECTURE.md` (Onboarding section)
