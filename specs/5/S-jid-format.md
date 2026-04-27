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
<platform>:<resource>/<id>[?<key>=<value>...]
```

Optional query params carry realm / scope / account when needed. The
form is a valid URI (`scheme:path?query`).

Examples:

```
telegram:user/123                       # DM
telegram:group/-456                     # group
discord:channel/1234?realm=67890        # guild channel (realm = guild_id)
discord:dm/1234                         # DM
whatsapp:group/123?realm=g.us           # group (realm = server)
reddit:comment/xyz                      # comment (was reddit:t1_xyz)
mastodon:status/abc?realm=mastodon.social
```

## Resource taxonomy

Each adapter owns its taxonomy. Core stores `resource` as free-form
string. Initial set per platform (extensible):

- **telegram**: `user`, `group`, `channel` (Telegram's `chat.type`:
  `private` → user, `group`/`supergroup` → group, `channel` → channel)
- **discord**: `channel`, `thread`, `dm`, `user`
- **whatsapp**: `group`, `user`
- **reddit**: `comment`, `submission`, `user`, `dm`
- **mastodon**: `user`, `status`
- **bluesky**: `user`, `post`
- **twitter**: `user`, `tweet`
- **linkedin**: `user`, `post`
- **email**: `address`, `thread`
- **web**: `slink`, `user`

## Code types

```go
type JID struct {
    Platform string
    Resource string
    ID       string
    Realm    string  // optional, query-param value if present
}

type ChatJID JID  // distinct named type, resource ∈ chat-kind set
type UserJID JID  // distinct named type, resource = "user"

func ParseChatJID(s string) (ChatJID, error)
func ParseUserJID(s string) (UserJID, error)
func (j JID) String() string
```

`Message.ChatJID` becomes `ChatJID`. `Message.Sender` becomes `UserJID`.
The compiler refuses to swap them. Parsers reject cross-kind strings.

## Design discipline

- **No legacy.** Hard cutover. One-shot migration rewrites every
  `messages.chat_jid`, `messages.sender`, `messages.routed_to`, and
  `routes.match` value (parallel pattern to migration 0032 invitations →
  invites). Old route forms (`room=12345`) become `resource=group id=12345`
  in the new schema.
- **One URL = one resource.** Discrimination at both layers — resource
  in the path (wire form), distinct type (code form).
- **Adapter-local parse OK.** `core.ParseChatJID` / `core.ParseUserJID`
  handle the canonical form. Adapters keep their own helpers for
  platform-side construction (snowflake widths, sign-bit hacks, server
  suffixes). Contract: emit canonical form on inbound; how you build it
  internally is private.

## Routing

`router/router.go:msgField` keys become `platform`, `resource`, `id`,
`realm`, `chat_jid`, `sender`, `verb`. Glob match unchanged. Existing
`room=` retired.

## Sequencing

1. `core` types + `Parse*` + tests.
2. `chanlib.InboundMsg` retyped; `router.msgField` extracts from typed value.
3. Adapters one at a time — build typed JID at inbound, destructure for outbound API calls.
4. `gateway`, `ipc`, `dashd` retype; migration rewrites stored values.
5. Concept doc `template/web/pub/concepts/jid.html` ships with the result.
