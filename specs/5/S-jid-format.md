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

## Adjacent open evolution work

Four design debts surfaced when wiring Discord and re-reading the JID
system end-to-end (2026-04-27 session). Each is independently shippable
but they share one direction — give the JID a typed model with explicit
resource semantics. Pick up together when JID gets focus.

### 1. Chat-JID vs user-JID as distinct types

Today `core.Message.ChatJID` and `core.Message.Sender` are both `string`,
both happen to use `<platform>:<rest>` shape — but they identify
different things (a chat with its history scope vs a user identity).
Nothing in the type system enforces the distinction; nothing prevents
passing a sender where a chat-jid is expected.

Move to `core.ChatJID` and `core.UserJID` as distinct named types with
their own validators + `Owns(...)` semantics. DB stays TEXT — wire
format unchanged. Catches a class of mistake at compile time and
clarifies intent at every call site.

### 2. Unified JID representation (struct, not raw string)

Every consumer parses the JID slightly differently today (`TrimPrefix`,
first-`:`-split, `JidPlatform`/`JidRoom` helpers, ad-hoc `@` parsing in
WhatsApp). Format invariants are folklore.

Introduce a typed struct with `String()` + `Parse*` boundary. All code
paths get fields. DB stores the string form; HTTP/MCP marshal the
string form. `MarshalJSON`/`UnmarshalJSON` keep the wire format
unchanged (pure refactor).

```go
type ChatJID struct {
    Platform string  // "discord", "telegram", ...
    Resource string  // "channel", "thread", "comment", "group", ...
    ID       string  // platform-native id
    Realm    string  // guild_id, "g.us", "lid", "dm", "" if N/A
}
```

Pairs with (1): once typed, a struct is the natural carrier.

### 3. Consistent resource-typed naming across platforms

Reddit's `t1_/t2_/t3_/t4_` kind-prefix is platform-internal numbering
exposed as the system's canonical representation. Other platforms
(mastodon, twitter) embed similar distinctions implicitly via context.
Normalize to `<platform>:<resource>/<id>[@<realm>]` — readable,
greppable, type-discriminated at parse time:

| Today                          | Proposed                                                                      |
| ------------------------------ | ----------------------------------------------------------------------------- |
| `reddit:t1_xyz`                | `reddit:comment/xyz`                                                          |
| `reddit:t3_abc`                | `reddit:submission/abc`                                                       |
| `reddit:t4_def`                | `reddit:dm/def`                                                               |
| `reddit:t2_<user>`             | `reddit:user/<user>`                                                          |
| `mastodon:<account_id>`        | `mastodon:user/<account_id>` (notification context) or `mastodon:status/<id>` |
| `discord:1234`                 | `discord:channel/1234@<guild>` or `discord:thread/1234@<guild>`               |
| `telegram:1234` (DM)           | `telegram:user/1234`                                                          |
| `telegram:-1234` (group)       | `telegram:group/1234`                                                         |
| `whatsapp:1234@g.us`           | `whatsapp:group/1234@g.us`                                                    |
| `whatsapp:1234@s.whatsapp.net` | `whatsapp:user/1234@phone`                                                    |

`<resource>` is the same field as `ChatJID.Resource` from (2).

Backwards-incompat for any DB row already written; routing rules in
production DBs would need rewriting (parallel to migration 0032 for
invitations → invites). Done early — before Discord rooms accumulate at
scale — minimizes blast radius.

### 4. Always-include guild in Discord JID

Discord's `m.GuildID` is read once at inbound, used only to flip the
`IsGroup` boolean, then discarded. Guild is available on every guild
message — including it in the JID has no usability cost (the agent
echoes back what it received) and unlocks per-guild routing. DM channels
get an explicit `@dm` realm.

Slots into (3) cleanly: `discord:channel/<channel_id>@<guild_id>` for
guild messages, `discord:channel/<channel_id>@dm` for DMs.

### Sequencing

(2) is the foundation — types come first. (1) follows naturally on (2).
(3) is the most invasive (every JID written changes shape) but most
clarifying once landed. (4) is a special case of (3) for Discord —
trivial after (3). Account segment from this spec's main body slots in
as `Realm` for platforms where account-disambiguation matters (telegram
multi-bot, whatsapp linked-id).

Doc home for the result: `template/web/pub/concepts/jid.html` — a
concepts page describing the typed model. Drafted as a writeup in the
2026-04-27 session; not shipped because describing today's string-prefix
state would have to be retracted once this work lands.
