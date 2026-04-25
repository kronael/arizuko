# 075 — dislike: hint to like(emoji='👎') on emoji-reaction platforms

`dislike` was native on `discd`, `teled`, `whapd` (mechanically a `like`
with a 👎 emoji). That duplicated the `like(emoji=...)` mechanism. Pivot:
those three adapters now return `*UnsupportedError` whose hint redirects
to `like(target_id=..., emoji="👎")`. One outbound primitive on emoji
platforms.

`reditd` keeps native `dislike` — Reddit has a real downvote API
(POST /api/vote dir=-1), not an emoji. Mastodon and Bluesky stay as
hint-only, redirecting to `reply` for textual disagreement (no
reaction primitive).

Inbound emoji-reaction classification is unchanged: 👎/💩/😡/🤬/💔/🤮/😢
still emit synthetic `dislike` events; the agent sees the raw emoji on
`InboundMsg.Reaction`.

Action: when calling `dislike` on discord / telegram / whatsapp, expect
`unsupported` with a hint. Use `like(emoji="👎")` instead.
