---
status: shipped
---

# slakd — Slack channel adapter

Bot-token, single-workspace. Same shape as `discd` / `teled`: registers
with routd via `chanlib.RouterClient`, exposes `/send`, `/like`,
`/delete`, `/upload`, `/health`. Ships as
`template/services/slakd.yml` + `slakd-routes.json`.

## Decisions worth keeping

**HTTP webhooks via proxyd, not Socket Mode.** Public reachability is
proxyd's job (`/slack/*` → `slakd:8080`), matching onbod / webd / dashd.
Socket Mode would be a second transport to maintain for no gain, so
there is no `SLAKD_PUBLIC_URL`.

**proxyd must forward the body bytes verbatim.** Slack signs
`v0:<ts>:<body>` with `SLACK_SIGNING_SECRET`; any re-marshal or TLS
re-sign in front of slakd breaks verification. Reject when
`|ts - now| > 5 min`.

**Subscribe to `message.*`, never `app_mention`.** Slack fires
`app_mention` _alongside_ `message.*`, so subscribing to both
double-delivers. Mirroring `discd`, slakd sets `Verb="mention"` when the
text contains `<@Uxxx>` matching `auth.test`'s `bot_user_id`, and `""`
otherwise. Files arrive piggy-backed on `message.*` via the `files`
array — do not subscribe to `file_shared` either, for the same reason.

**Threading rides `Topic`.** `Topic = thread_ts` on every threaded
inbound; top-level messages get `""`. Reconstruction matches the root by
`ID` and replies by `Topic`, so `get_thread` works with no
slakd-specific code — the same shape as Telegram forum topics and
Discord threads. Topic is opaque and never compared across platforms.

**Typing is a 👀 reaction**, not `conversations.typing` (RTM-only,
discontinued 2024) and not `assistant.threads.setStatus`. One code path
for panes and regular channels; `already_reacted` / `no_reaction` are
ignored; silent no-op when no prior inbound is known for the JID.

**File auth differs per platform, and only here.** Discord uses
time-signed CDN URLs and Telegram embeds the token in the URL path;
Slack needs `Authorization: Bearer $SLACK_BOT_TOKEN` on the upstream
fetch. Everything else is the standard `chanlib.URLCache` +
`GET /files/<id>` proxy, so the agent fetches a stable credential-free
URL and the Whisper path is identical to every other adapter.

`files.getUploadURLExternal` + `files.completeUploadExternal` —
`files.upload` was deprecated 2025-05.

## JID

`slack:<workspace>/<kind>/<id>`, kind ∈ `channel` | `dm` | `group`
(legacy mpim). IDs are verbatim from Slack so pasting from a Slack URL
works. `IsGroup` is false only for `dm`. Multi-workspace = one slakd per
workspace, disambiguated by the workspace segment.

## Reactions

`reaction_added` → `Verb: ClassifyEmoji(name)`, with the raw name in
both `Content` and `Reaction`. Names arrive without colons
(`thumbsup`). Workspace-custom emoji have no Unicode codepoint and fall
through `ClassifyEmoji`'s unknown→like default; the agent still gets the
name, which is enough for most flows. `reaction_removed` not emitted.

## Out of scope

OAuth install (manual install runbook in `slakd/README.md`;
multi-workspace token store is a separate spec), Enterprise Grid, slash
commands / shortcuts / modals / home tab / Block Kit, user tokens
(`xoxp-`), and custom-emoji-as-dislike (needs a per-workspace
`emoji.list` mapping).

Signing-secret rotation is startup-only, matching mastd. File uploads
post as the bot, not the agent persona.
