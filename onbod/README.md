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

## Entry points

- Binary: `onbod/main.go`
- Listen: `$ONBOD_LISTEN_ADDR` (default `:8080`)
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

## Files

- `main.go` — config, poll loop, HTTP handlers, OAuth wiring
- `integration_test.go` — end-to-end flow tests

## Related docs

- `specs/4/26-prototypes.md` (prototype mechanic)
- `specs/5/28-mass-onboarding.md`
- `ARCHITECTURE.md` (Onboarding section)
