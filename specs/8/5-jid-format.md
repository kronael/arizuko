# JID Format: platform:account/id

**Status**: planned (2026-03-25)

## Problem

Current JID format `platform:id` (e.g. `telegram:123456789`) cannot distinguish
which account a message arrived through when multiple accounts of the same
platform are running. Routing rules also can't express "this telegram bot" vs
"that telegram bot".

## New Format

```
platform:account/id
```

| Segment    | Example     | Notes                                    |
| ---------- | ----------- | ---------------------------------------- |
| `platform` | `telegram`  | lowercase platform name                  |
| `account`  | `mybot`     | platform-native account name (see below) |
| `id`       | `123456789` | platform-native chat or user ID          |

## Account Segment

The account segment comes from the **platform itself**, resolved after auth.
It is the human-readable name of the authenticated account:

| Adapter  | Account source                | Example                  |
| -------- | ----------------------------- | ------------------------ |
| `teled`  | `api.Self.UserName`           | `mybot`                  |
| `discd`  | `session.State.User.Username` | `mybot`                  |
| `mastd`  | `me.Acct`                     | `myacct@instance.social` |
| `bskyd`  | `cfg.Identifier` (handle)     | `handle.bsky.social`     |
| `reditd` | `cfg.Username`                | `myreddituser`           |
| `emaid`  | `cfg.Account` (email addr)    | `bot@example.com`        |

`CHANNEL_ACCOUNT` env var overrides if set — useful when the platform name is
too long, unstable, or you want a stable routing label independent of platform
identity.

### Examples

| Context       | JID                                      |
| ------------- | ---------------------------------------- |
| Telegram chat | `telegram:mybot/123456789`               |
| Telegram user | `telegram:mybot/456`                     |
| Discord chan  | `telegram:mybot/channel_id`              |
| Mastodon      | `mastodon:acct@instance.social/12345`    |
| Bluesky       | `bluesky:handle.bsky.social/did:plc:abc` |
| Reddit        | `reddit:myuser/golang`                   |
| Email         | `email:bot@example.com/deadbeef`         |
| Local (agent) | `local:support` (unchanged, no account)  |

`local:` JIDs have no account concept — they remain `local:folder`.

## Routing

JID prefixes registered with the router include the account:

```
telegram:mybot/    →   root group
telegram:support_bot/ →   support group
```

Rules match by prefix — any chat on that account routes to the mapped group.

## Parsing Helpers

Each adapter needs one helper to extract the native ID from a JID:

```go
func nativeID(jid, platform, account string) string {
    return strings.TrimPrefix(jid, platform+":"+account+"/")
}
```

For teled specifically, `parseChatID` becomes:

```go
func parseChatID(jid, account string) (int64, error) {
    return strconv.ParseInt(nativeID(jid, "telegram", account), 10, 64)
}
```

The account is resolved at startup (after auth) and stored on the bot/client
struct. All JID construction and parsing uses that value.

## Migration

No backward compatibility. Rebuild all adapters and restart. Update routing
rules in `.env` from `telegram:` → `telegram:mybot/`.

## Changes Required

| File                    | Change                                                 |
| ----------------------- | ------------------------------------------------------ |
| `teled/bot.go`          | account from `api.Self.UserName`; JID/sender format    |
| `teled/main.go`         | pass account to bot; prefix `["telegram:<acct>/"]`     |
| `teled/server.go`       | prefix in health response                              |
| `discd/bot.go`          | account from `session.State.User.Username`; JID/sender |
| `discd/main.go`         | pass account; prefix                                   |
| `discd/server.go`       | prefix in health response                              |
| `mastd/client.go`       | account from `me.Acct`; JID/sender format              |
| `mastd/main.go`         | prefix                                                 |
| `mastd/server.go`       | prefix in health response                              |
| `bskyd/client.go`       | account from `cfg.Identifier`; JID/sender              |
| `bskyd/main.go`         | prefix                                                 |
| `bskyd/server.go`       | prefix in health response                              |
| `reditd/client.go`      | account from `cfg.Username`; JID/sender                |
| `reditd/main.go`        | prefix                                                 |
| `reditd/server.go`      | prefix in health response                              |
| `emaid/imap.go`         | account from `cfg.Account`; JID/sender                 |
| `emaid/main.go`         | prefix                                                 |
| `emaid/server.go`       | `TrimPrefix("email:")` → `nativeID`                    |
| `gateway/gateway.go`    | `local:` prefix checks unchanged; escalation parse     |
| `gateway/local_channel` | no change                                              |
| `auth/oauth.go`         | `discord:sub` → `discord:<acct>/sub`                   |
| `ipc/ipc.go`            | `whatsapp:` prefix check                               |
