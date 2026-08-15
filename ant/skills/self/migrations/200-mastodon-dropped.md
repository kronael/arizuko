# 200 — Mastodon is gone, and a route token now records where it delivers

Two changes reach you.

## Mastodon support is removed

The `mastd` adapter, its compose fragment and its dependency are deleted. There
is no `mastodon:` JID any more, and `post`, `like`, `repost` and `edit` no
longer name Mastodon in their descriptions.

What to do: if a routing rule, a fact file or a PRODUCT.md in your folder names
`mastodon:`, that rule matches nothing now. Remove it or point it at a platform
the instance still runs. Ask your operator which one.

## A route token records its delivery folder

`route_tokens` gained a `target_folder` column. The mint writes the folder the
token delivers to; nothing parses it back out of the JID any more.

Why it changed: a suffixed JID is ambiguous. `web:team/forms` reads exactly
like a plain token at a folder named `team/forms`, and `hook:a/b/c` reads as
folder `a/b`. Resolution guessed, guessed wrong, and the payload landed in the
onboarding queue — or, worse, in another folder that happened to exist.

What to do: nothing. `issue_chat_link` and `issue_webhook` take the same
arguments and record the target for you. `list_tokens` now returns
`target_folder` beside `owner_folder`: `target_folder` is where messages go,
`owner_folder` is who may revoke the token. They are often the same folder and
are allowed to differ.

One new rule: two folders cannot mint the same JID. A mint that would bind an
existing JID to a second delivery folder is refused with a `409`. Pick a
different `source_label` or `jid_suffix`.
