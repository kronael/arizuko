---
status: draft
---

# JID Format: platform:account/id

**Status**: planned (2026-03-25)

## Problem

Current JID format `platform:id` cannot distinguish which account a message
arrived through. Routing rules can't express "this telegram bot" vs "that one".

## New Format

```
platform:account/id
```

| Segment    | Example     | Notes                                |
| ---------- | ----------- | ------------------------------------ |
| `platform` | `telegram`  | lowercase, fixed per adapter type    |
| `account`  | `mybot`     | resolved from platform after connect |
| `id`       | `123456789` | platform-native chat or user ID      |

`local:` JIDs are internal and have no account segment — they remain `local:folder`.

## Account Segment

Resolved from the platform **after** connecting, not from config:

| Adapter  | Account source                | Example                |
| -------- | ----------------------------- | ---------------------- |
| `teled`  | `api.Self.UserName`           | `mybot`                |
| `discd`  | `session.State.User.Username` | `mybot`                |
| `mastd`  | `me.Acct`                     | `acct@instance.social` |
| `bskyd`  | `cfg.Identifier` (handle)     | `handle.bsky.social`   |
| `reditd` | `cfg.Username`                | `myuser`               |
| `emaid`  | `cfg.Account` (email addr)    | `bot@example.com`      |

`CHANNEL_ACCOUNT` env var overrides — use when the platform name is too long,
changes, or you want a stable label. Not required.

## Registration

Channels register with `platform` + `account`. The router derives the JID
prefix `platform:account/` automatically — no `jid_prefixes` field needed.

```json
POST /v1/channels/register
{
  "platform": "telegram",
  "account":  "mybot",
  "url":      "http://teled:9001",
  "capabilities": { "send_text": true, "send_file": true, "typing": true }
}
```

Router key: `(platform, account)` — unique per authenticated account.

## Multiple Daemons, Same Platform

Each daemon connects as a different account → different `(platform, account)`
tuple → separate registration, separate JID prefix, independent routing.

```
teled instance 1  platform=telegram  account=mybot      → telegram:mybot/*
teled instance 2  platform=telegram  account=supportbot → telegram:supportbot/*
```

One TOML file per daemon in `services/`. No special config — the account
comes from the bot token itself after auth.

```toml
# services/teled-support.toml
image = "arizuko:latest"
entrypoint = ["teled"]
[environment]
ROUTER_URL   = "http://gated:${API_PORT}"
TELEGRAM_BOT_TOKEN = "${TELEGRAM_SUPPORT_BOT_TOKEN}"
CHANNEL_SECRET     = "${CHANNEL_SECRET}"
LISTEN_ADDR  = ":9002"
LISTEN_URL   = "http://teled-support:9002"
```

Router registers both entries. Routing rules:

```
ROUTE_telegram:mybot/*      = root
ROUTE_telegram:supportbot/* = support
```

## Channel Interface

```go
type Channel interface {
    Platform() string  // "telegram" — fixed, declared by adapter
    Account()  string  // "mybot" — resolved after Connect()
    Connect(ctx context.Context) error
    Owns(jid string) bool
    Send(jid, text, replyTo, threadID string) (string, error)
    SendFile(jid, path, name string) error
    Typing(jid string, on bool) error
    Disconnect() error
}
```

`Owns` is derived: `strings.HasPrefix(jid, platform+":"+account+"/")`.

## Parsing Helpers

```go
func nativeID(jid, platform, account string) string {
    return strings.TrimPrefix(jid, platform+":"+account+"/")
}

// teled
func parseChatID(jid, account string) (int64, error) {
    return strconv.ParseInt(nativeID(jid, "telegram", account), 10, 64)
}
```

Account is stored on the bot struct after `Connect()`. All JID construction
and parsing reads from there.

## Migration

No backward compatibility. Rebuild all adapters and restart.
Update routing rules: `telegram:` → `telegram:mybot/`.

## Changes Required

| File                 | Change                                                                 |
| -------------------- | ---------------------------------------------------------------------- |
| `core/types.go`      | Add `Platform()`, `Account()` to `Channel` interface                   |
| `chanreg/chanreg.go` | Key on `(platform, account)`; derive prefix; drop `jid_prefixes` field |
| `api/api.go`         | Register endpoint: accept `platform`+`account`, not `jid_prefixes`     |
| `teled/bot.go`       | Store account from `api.Self.UserName`; JID/sender format              |
| `teled/main.go`      | Pass account to bot after auth; register with platform+account         |
| `teled/server.go`    | Health: return `platform`+`account`, drop `jid_prefixes`               |
| `discd/bot.go`       | Account from `session.State.User.Username`; JID/sender                 |
| `discd/main.go`      | Register with platform+account                                         |
| `discd/server.go`    | Health response                                                        |
| `mastd/client.go`    | Account from `me.Acct`; JID/sender                                     |
| `mastd/main.go`      | Register                                                               |
| `mastd/server.go`    | Health                                                                 |
| `bskyd/client.go`    | Account from `cfg.Identifier`; JID/sender                              |
| `bskyd/main.go`      | Register                                                               |
| `bskyd/server.go`    | Health                                                                 |
| `reditd/client.go`   | Account from `cfg.Username`; JID/sender                                |
| `reditd/main.go`     | Register                                                               |
| `reditd/server.go`   | Health                                                                 |
| `emaid/imap.go`      | Account from `cfg.Account`; JID/sender                                 |
| `emaid/main.go`      | Register                                                               |
| `emaid/server.go`    | Health; `TrimPrefix("email:")` → `nativeID`                            |
| `chanlib/chanlib.go` | Registration helper: `platform`+`account` instead of `jid_prefixes`    |
| `gateway/gateway.go` | `local:` prefix checks unchanged                                       |
| `auth/oauth.go`      | `discord:sub` → `discord:<acct>/sub`                                   |
| `ipc/ipc.go`         | `whatsapp:` prefix check → `whatsapp:<acct>/`                          |
