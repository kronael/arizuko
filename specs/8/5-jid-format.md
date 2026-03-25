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

| Segment    | Example     | Notes                           |
| ---------- | ----------- | ------------------------------- |
| `platform` | `telegram`  | lowercase platform name         |
| `account`  | `main`      | label set via `CHANNEL_ACCOUNT` |
| `id`       | `123456789` | platform-native chat or user ID |

### Examples

| Context       | Old                   | New                         |
| ------------- | --------------------- | --------------------------- |
| Telegram chat | `telegram:123456789`  | `telegram:main/123456789`   |
| Telegram user | `telegram:456`        | `telegram:main/456`         |
| Discord chan  | `discord:channel_id`  | `discord:main/channel_id`   |
| Mastodon acct | `mastodon:12345`      | `mastodon:main/12345`       |
| Bluesky DID   | `bluesky:did:plc:abc` | `bluesky:main/did:plc:abc`  |
| Reddit sub    | `reddit:golang`       | `reddit:main/golang`        |
| Email thread  | `email:deadbeef`      | `email:main/deadbeef`       |
| Local (agent) | `local:support`       | `local:support` (unchanged) |

`local:` JIDs have no account concept — they remain `local:folder`.

## Account Label

Each adapter reads `CHANNEL_ACCOUNT` env var. Default: `"main"`.

```
CHANNEL_ACCOUNT=support   →   telegram:support/123456789
```

Routing rules match by prefix:

```
telegram:main/*    →   root group
telegram:support/* →   support group
```

JID prefixes registered with the router use the account label:

```go
[]string{"telegram:main/"}
```

## Parsing Helpers

Each adapter needs one helper to extract the native ID from a JID:

```go
// generic
func nativeID(jid, platform, account string) string {
    prefix := platform + ":" + account + "/"
    return strings.TrimPrefix(jid, prefix)
}
```

For teled specifically, `parseChatID` becomes:

```go
func parseChatID(jid, account string) (int64, error) {
    return strconv.ParseInt(nativeID(jid, "telegram", account), 10, 64)
}
```

## Migration

No backward compatibility. All running instances must update `.env` with
`CHANNEL_ACCOUNT=main` (or the label they want), rebuild adapters, and restart.
Existing routing rules in `.env` must be updated to use `platform:main/` prefix.

## Changes Required

| File                      | Change                                          |
| ------------------------- | ----------------------------------------------- |
| `teled/bot.go`            | JID/sender format, `parseChatID` signature      |
| `teled/main.go`           | prefix `["telegram:main/"]`, load account       |
| `teled/server.go`         | prefix in health response                       |
| `discd/bot.go`            | JID/sender, `TrimPrefix` → `nativeID`           |
| `discd/main.go`           | prefix, load account                            |
| `discd/server.go`         | prefix in health response                       |
| `mastd/client.go`         | JID/sender format                               |
| `mastd/main.go`           | prefix, load account                            |
| `mastd/server.go`         | prefix in health response                       |
| `bskyd/client.go`         | JID/sender format                               |
| `bskyd/main.go`           | prefix, load account                            |
| `bskyd/server.go`         | prefix in health response                       |
| `reditd/client.go`        | JID/sender format                               |
| `reditd/main.go`          | prefix, load account                            |
| `reditd/server.go`        | prefix in health response                       |
| `emaid/imap.go`           | JID/sender format                               |
| `emaid/main.go`           | prefix, load account                            |
| `emaid/server.go`         | `TrimPrefix("email:")` → `nativeID`             |
| `gateway/gateway.go`      | `local:` prefix checks remain, escalation parse |
| `gateway/local_channel`   | no change (`local:folder` format unchanged)     |
| `auth/oauth.go`           | `discord:sub` → `discord:main/sub` etc.         |
| `ipc/ipc.go`              | `whatsapp:` prefix check                        |
| `specs/8/4-multi-account` | update examples to new format                   |
