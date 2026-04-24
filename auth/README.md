# auth

Identity, JWT, OAuth, authorization policy, HTTP middleware.

## Purpose

Shared auth primitives used across daemons. Three concerns: (1) user
auth (password argon2, JWT sessions, OAuth providers, Telegram widget),
(2) runtime identity resolution for agents (`Identity` from folder and
tier), (3) tool-level authorization via `Authorize`.

## Public API

- Identity: `Identity`, `Resolve(folder string) Identity`, `WorldOf(folder)`, `IsDirectChild(parent, child)`, `CheckSpawnAllowed`
- Authorization: `Authorize(id Identity, tool string, target AuthzTarget) error`, `AuthzTarget`, `MatchGroups(allowed, folder)`
- JWT: `Claims`, `VerifyJWT(secret, token)`, JWT issuing helpers
- OAuth: GitHub, Google, Discord, Telegram widget — shared `createOAuthSession`
- HMAC: `SignHMAC`, `VerifyHMAC`, `UserSigMessage`, `SlinkSigMessage`, `VerifyUserSig`, `VerifySlinkSig`
- Password: `HashToken`, argon2 verify
- Middleware: `RegisterRoutes(mux, store, cfg)` mounts `/auth/*`

## Dependencies

- `core`, `store`

## Configuration

- `AUTH_SECRET`, `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GITHUB_ALLOWED_ORG`
- `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_ALLOWED_EMAILS`
- `DISCORD_CLIENT_ID/SECRET`
- `TELEGRAM_BOT_TOKEN` (widget verification)

## Files

- `identity.go` — tier/world resolution, spawn rules
- `policy.go` — `Authorize` per tool
- `acl.go` — `MatchGroups`, glob ACL
- `jwt.go`, `middleware.go`, `web.go` — session handling + login routes
- `oauth.go` — provider dance
- `hmac.go` — inter-daemon header signing

## Related docs

- `ARCHITECTURE.md` (Auth Hardening)
- `specs/7/28-acl.md`
