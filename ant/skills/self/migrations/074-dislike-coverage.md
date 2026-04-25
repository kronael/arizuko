# 074 — like/dislike coverage expanded

`dislike` is now native on `reditd`, `teled`, `whapd` (joining `discd`).
`like` is now native on `reditd` (Reddit upvote), `teled`
(setMessageReaction 👍), `whapd` (Baileys reaction).

Inbound emoji reactions emit synthetic `like` / `dislike` events on
`discd`, `teled`, `whapd`. `InboundMsg.Reaction` carries the raw emoji.
Negative emoji set: 👎 💩 😡 🤬 💔 🤮 😢 — anything else maps to `like`.

Mastodon and Bluesky still have no `dislike` primitive (no platform
support); they continue to return unsupported with a hint. Reaction
removal is not propagated — only additions trigger events.

Action: when calling `dislike` on reddit / telegram / whatsapp, expect
success now instead of `ErrUnsupported`. The hint redirect is gone for
those three.
