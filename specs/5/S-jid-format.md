---
status: shipped
shipped: 2026-05-01
---

# Typed JID — one resource per address

> **Descope note (2026-05-28).** The wire form and glob semantics below
> shipped and are canonical. The _typed Go structs_ this spec proposed
> (`JID`/`ChatJID`/`UserJID` over `*url.URL`, with `ParseJID`/`MatchJID`)
> were **not built** — production keeps JIDs as plain `string`s, split by
> `core.JidPlatform`/`core.JidRoom` (`core/types.go`), matched by
> `router.RouteMatches`. `core/jid.go` was deleted. The compiler does not
> stop you swapping a chat for a sender; ingress does
> ([`34-channel-protocol.md`](34-channel-protocol.md) §"`chat_jid` and
> `sender` are different shapes").

> **Forward pointer.** [`5/29`](29-worlds-guests-oauth.md) collapses
> tenancy to World → Agent → Session and leaves open how those address
> onto this wire form: folders (`world/agent`) are route _targets_, not
> JIDs, so the question is whether a session stays addressed by `run_id`
> or earns a JID segment. Nothing below changes until that resolves.

## Problem

A JID identifies one resource on one platform, but it was a `string`
with ad-hoc per-platform syntax and multiple resource kinds colliding on
one prefix: `telegram:1234` was user-DM _or_ group with a sign-bit hack
disambiguating; `reddit:t1_xyz` and `reddit:t2_<user>` shared `reddit:`.

## Wire form

```
<platform>:<rest>
```

`<platform>` is the adapter's name, lowercase, no colons. `<rest>` is
**platform-private** — each adapter declares and parses its own schema;
core treats it as opaque except for `path.Match` globbing over `/`.

**Kind discrimination lives in the first `<rest>` segment**, as fixed
positional values, not labels: `telegram:user/<id>` vs
`telegram:group/<id>`, `discord:dm/<id>` vs `discord:<guild>/<channel>`,
`reddit:{comment,submission,dm,user}/<id>`, `email:{address,thread}/…`,
`web:<folder>`, `hook:<folder>/<source>`. WhatsApp already encoded kind
in `@<server>` (`g.us` / `s.whatsapp.net` / `lid`) and Twitter already
carried a kind segment — neither adapter was touched.

**A kind earns its place when someone treats it differently** from its
siblings on the same platform. Adding one later is a one-string change in
the adapter: no system-wide format change, no migration of existing rows.

## Routing

`router/router.go` `msgField` keys: `platform`, `room`, `chat_jid`,
`sender`, `verb`. Glob is `path.Match`, **uniform across every key**:

| filter        | matches                                         |
| ------------- | ----------------------------------------------- |
| `key=<exact>` | value equals `<exact>`                          |
| `key=<glob>`  | `*` `?` `[…]`, where `*` does **not** cross `/` |
| `key=*`       | value is present (non-empty)                    |
| `key=`        | value is absent (empty)                         |
| (key omitted) | unconstrained                                   |

`*` stopping at `/` is what makes segments first-class:
`chat_jid=telegram:group/*` is all Telegram groups,
`chat_jid=discord:67890/*` is one guild's channels and threads.

## Design discipline

- **No legacy in storage — hard cutover.** Migrations
  `0042-typed-jids.sql` + `0043-typed-jids-tail.sql` rewrote every
  JID-shaped value in place (`messages.chat_jid`/`sender`/
  `reply_to_sender`, `chats.jid`, `user_jids.jid`, `grants.jid`,
  `onboarding.jid`, `scheduled_tasks.chat_jid`, and the `chat_jid=` /
  `sender=` / `room=` predicates inside `routes.match`). Every UPDATE was
  guarded by `NOT LIKE` on the new shape, so re-running is a no-op.
  `messages.routed_to` holds folder paths, not JIDs — left alone.
- **Discord legacy rows have no stored guild id** and migrated to the
  placeholder `discord:_/<channel>`. New inbound carries the real guild;
  outbound that needs it reads chat metadata, not the JID.
- **`web:` stays folder-keyed.** `web:<folder>` is the chat identity the
  web stack uses; it did **not** migrate to `web:slink/<token>` /
  `web:user/<sub>`. Splitting token-vs-sub identity in a JID is deferred
  until the web stack itself splits them — today the route token carries
  routing and the JWT overlay carries identity, orthogonally
  ([`W-webhook-routes.md`](W-webhook-routes.md)).
- **Adapters must emit canonical form on inbound**; outbound accepted
  both forms during the cutover so deployed bots didn't break mid-flight.
