---
status: unshipped
---

# Typed JID — single resource per URL

A JID identifies one resource on one platform. Today it's a `string` with
ad-hoc per-platform syntax; multiple resource kinds collide on the same
prefix (`telegram:1234` is user-DM or group, sign-bit hack disambiguates;
`reddit:t1_xyz` and `reddit:t2_<user>` share the `reddit:` prefix). And
`messages.chat_jid` / `messages.sender` are both `string` despite
identifying chats vs users.

## Wire form

```
<platform>:<kind>/<id>[/<kind>/<id>]*
```

A sequence of `<kind>/<id>` pairs after the platform colon. At least
one pair. The last pair is the primary resource; earlier pairs are
scope (guild, server, instance, account, etc.). Variable depth per
platform.

Examples:

```
telegram:user/123                                # DM
telegram:group/-456                              # group
discord:guild/67890/channel/1234                 # guild channel
discord:guild/67890/thread/9999                  # thread in guild
discord:dm/12345                                 # DM channel (kind=dm, id=channel-id)
discord:user/777                                 # sender
whatsapp:server/g.us/group/1234                  # group
whatsapp:server/s.whatsapp.net/user/12133        # phone DM
mastodon:instance/mastodon.social/user/1234      # account on instance
mastodon:instance/mastodon.social/status/abc     # status on instance
reddit:comment/xyz                               # comment (was reddit:t1_xyz)
reddit:user/foo                                  # sender
bluesky:user/did:plc:xyz                         # IDs may contain ':'; only '/' separates segments
```

## Kind taxonomy

Each adapter owns its kinds. Core stores them as free-form strings.
Initial set per platform (extensible without format change):

- **telegram**: `user`, `group` (channel folds into group until broadcast-specific behavior matters)
- **discord**: scope `guild`/`dm`; resource `channel`/`thread`/`user`
- **whatsapp**: scope `server`; resource `group`/`user`
- **reddit**: `comment`, `submission`, `user`, `dm` (subreddit scope optional)
- **mastodon**: scope `instance`; resource `user`/`status`
- **bluesky**: `user`, `post`
- **twitter**: `user`, `tweet`
- **linkedin**: `user`, `post`
- **email**: `address`, `thread`
- **web**: `slink`, `user`

A kind earns its place when an adapter, the gateway, or the agent
treats it differently from siblings. No different treatment → fold it.
Adding a kind later is a one-string change in the adapter, no JID
format or DB migration.

## Code types

```go
type Pair struct{ Kind, ID string }

type JID struct {
    Platform string
    Path     []Pair  // last is primary resource; earlier are scope
}

type ChatJID JID  // resource kind ∈ chat-kind set (channel, thread, group, dm, status, comment, ...)
type UserJID JID  // resource kind = "user"

func ParseChatJID(s string) (ChatJID, error)
func ParseUserJID(s string) (UserJID, error)
func (j JID) String() string
func (j JID) Resource() Pair  // last pair
func (j JID) Scope() []Pair   // pairs before last
```

`Message.ChatJID` becomes `ChatJID`. `Message.Sender` becomes `UserJID`.
The compiler refuses to swap them. Parsers reject cross-kind strings.

## Design discipline

- **No legacy.** Hard cutover. One-shot migration rewrites every
  `messages.chat_jid`, `messages.sender`, `messages.routed_to`, and
  `routes.match` value (parallel pattern to migration 0032 invitations →
  invites).
- **One URL = one resource.** Discrimination at both layers — resource
  in the path (wire form), distinct type (code form).
- **Adapter-local parse OK.** `core.ParseChatJID` / `core.ParseUserJID`
  handle the canonical form. Adapters keep their own helpers for
  platform-side construction (snowflake widths, sign-bit hacks, server
  suffixes). Contract: emit canonical form on inbound; how you build it
  internally is private.

## Routing

`router/router.go:msgField` keys: `platform`, `chat_jid`, `sender`,
`verb`, plus `tail_kind` (last pair's kind = primary resource type) and
`tail_id` (last pair's id). Scope filtering uses `chat_jid=` glob —
e.g. `chat_jid=discord:guild/67890/*` matches anything in that guild.

Glob semantics, uniform across all keys:

| filter        | matches                                                            |
| ------------- | ------------------------------------------------------------------ |
| `key=<exact>` | value equals `<exact>`                                             |
| `key=<glob>`  | value matches glob (path.Match: `*` `?` `[…]`)                     |
| `key=*`       | value is **present** (non-empty) — bare `*` silently rejects empty |
| `key=`        | value is **absent** (empty)                                        |
| (omit key)    | unconstrained — no filter on this field                            |

## Sequencing

1. `core` types + `Parse*` + tests.
2. `chanlib.InboundMsg` retyped; `router.msgField` extracts from typed value.
3. Adapters one at a time — build typed JID at inbound, destructure for outbound API calls.
4. `gateway`, `ipc`, `dashd` retype; migration rewrites stored values.
5. Concept doc `template/web/pub/concepts/jid.html` ships with the result.
