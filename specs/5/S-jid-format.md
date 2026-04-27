---
status: unshipped
---

# Typed JID — single resource per URL

A JID identifies one resource on one platform. Today it's a `string`
with ad-hoc per-platform syntax; multiple resource kinds collide on
the same prefix (`telegram:1234` is user-DM or group, sign-bit hack
disambiguates; `reddit:t1_xyz` and `reddit:t2_<user>` share the
`reddit:` prefix). And `messages.chat_jid` / `messages.sender` are
both `string` despite identifying chats vs users.

## Wire form

```
<platform>:<rest>
```

`<platform>` is the adapter's name (lowercase, no colons). `<rest>`
is platform-private — each adapter declares its own fixed schema.
Adapters parse their own; core treats `<rest>` as opaque except for
the `path.Match` glob semantics over `/` separators.

## Per-platform schemas

Each adapter documents its `<rest>` shape. Fixed positional segments
(no labels), separated by `/`, with kind discrimination encoded in
distinct first-segment values:

```
telegram:user/<chat_id>                          # DM (chat_id positive)
telegram:group/<chat_id>                         # group/supergroup/channel
telegram:user/<user_id>                          # sender (same shape; routed by message context)

discord:<guild_id>/<channel_id>                  # guild text channel or thread
discord:dm/<channel_id>                          # DM channel
discord:user/<user_id>                           # sender

whatsapp:<id>@<server>                           # server distinguishes group/dm/lid
                                                 #  (g.us, s.whatsapp.net, lid)

mastodon:<instance_host>/<account_id>            # account on instance
mastodon:<instance_host>/status/<status_id>      # status (toot)

reddit:comment/<id>                              # comment
reddit:submission/<id>                           # submission
reddit:dm/<id>                                   # modmail
reddit:user/<username>                           # sender

bluesky:user/<did>                               # bluesky user (DIDs contain ':')
bluesky:post/<at_uri>                            # bluesky post

twitter:user/<user_id>
twitter:tweet/<tweet_id>

linkedin:user/<urn>
linkedin:post/<urn>

email:address/<addr>                             # sender
email:thread/<msgid>                             # thread (message-id of root)

web:slink/<token>                                # anonymous web chat
web:user/<sub>                                   # authed web sender
```

A kind earns its place when an adapter, the gateway, or the agent
treats it differently from siblings on the same platform. Adding a
kind later is a one-string change in the adapter — no system-wide
format change, no DB migration of existing rows.

## Code types

The JID is a valid URI — opaque-path form per RFC 3986. Build on
`net/url`:

```go
type JID struct{ u *url.URL }

func ParseJID(s string) (JID, error)         // wraps url.Parse + validation
func (j JID) Platform() string               // u.Scheme
func (j JID) Path() string                   // u.Opaque (adapter splits further)
func (j JID) String() string                 // u.String (handles percent-encoding)

type ChatJID struct{ JID }                   // resource is a chat/destination kind
type UserJID struct{ JID }                   // resource is a user identity

func ParseChatJID(s string) (ChatJID, error)
func ParseUserJID(s string) (UserJID, error)
```

`Message.ChatJID` becomes `ChatJID`. `Message.Sender` becomes
`UserJID`. The compiler refuses to swap them. `ParseChatJID` and
`ParseUserJID` validate kind by inspecting the first path segment.

Why net/url: free URI-spec compliance; free percent-encoding for
platform IDs that contain reserved chars (Bluesky DIDs contain `:`);
future-extensible if query params or fragments are ever needed.

Adapters keep their own helpers for platform-side construction
(snowflake widths, sign-bit hacks, server suffixes) and platform-side
parsing of `j.Path()` per their schema. Core just guarantees a valid
URI shape.

## Routing

`router/router.go:msgField` keys: `platform`, `chat_jid`, `sender`,
`verb`. Glob match uses `path.Match` — `*` matches any non-`/`
sequence (so segments are first-class), `?` one non-`/` char, `[…]`
character class.

Examples:

```
match='platform=telegram chat_jid=telegram:group/*'    # all telegram groups
match='chat_jid=discord:67890/*'                       # guild 67890, any channel/thread
match='chat_jid=discord:dm/*'                          # all Discord DMs
match='chat_jid=whatsapp:*@g.us'                       # all whatsapp groups
match='chat_jid=mastodon:mastodon.social/*'            # all activity on that instance
```

Glob semantics, uniform across all keys:

| filter        | matches                                       |
| ------------- | --------------------------------------------- |
| `key=<exact>` | value equals `<exact>`                        |
| `key=<glob>`  | value matches glob (`*` `?` `[…]`, `*` ≠ `/`) |
| `key=*`       | value is **present** (non-empty)              |
| `key=`        | value is **absent** (empty)                   |
| (omit key)    | unconstrained — no filter on this field       |

## Design discipline

- **No legacy.** Hard cutover. One-shot migration rewrites every
  `messages.chat_jid`, `messages.sender`, `messages.routed_to`, and
  `routes.match` value (parallel pattern to migration 0032
  invitations → invites).
- **One URL = one resource.** Discrimination at both layers — kind
  in the path (wire form), distinct type (code form).
- **Adapter-local parse OK.** Core's `ParseChatJID` / `ParseUserJID`
  validate platform prefix and non-empty path; deeper structure is
  the adapter's contract. Adapters MUST emit canonical form on
  inbound and parse it on outbound.

## Sequencing

1. `core` types + `Parse*` + tests.
2. `chanlib.InboundMsg` retyped; `router.msgField` extracts from typed value.
3. Adapters one at a time — build typed JID at inbound, destructure for outbound API calls.
4. `gateway`, `ipc`, `dashd` retype; migration rewrites stored values.
5. Concept doc `template/web/pub/concepts/jid.html` ships with the result.
