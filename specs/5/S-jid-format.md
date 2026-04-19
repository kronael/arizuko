---
status: unshipped
---

# JID format: platform:account/id

Extend JID with an account segment resolved from the platform after
`Connect()` (e.g. `api.Self.UserName` for telegram,
`session.State.User.Username` for discord). Channel registers with
`(platform, account)`; router derives prefix. `CHANNEL_ACCOUNT` env
var overrides when the platform name is unstable.

Channel interface adds `Platform()` + `Account()`; `Owns` derived as
`HasPrefix(jid, platform+":"+account+"/")`. `local:` JIDs unchanged
(no account).

No backward compatibility — rebuild all adapters; update routing rules
(`telegram:` → `telegram:mybot/`).

Rationale: current `platform:id` can't distinguish accounts — routing
rules can't express "this telegram bot" vs "that one". Prerequisite
for [9-identities.md](9-identities.md) and
[R-multi-account.md](R-multi-account.md).

Unblockers: full cross-adapter change list in `core/types.go`,
`chanreg`, `api`, every `{teled,discd,mastd,bskyd,reditd,emaid}/bot.go`

- `main.go` + `server.go`, `chanlib`, `auth/oauth.go`, `ipc/ipc.go`.
