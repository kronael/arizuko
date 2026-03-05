# TODO

## memory

- collapse `sessions` table into `registered_groups.session_id` column (see specs/v1/db-bootstrap.md)
- test SDK resume failure: send bad session ID to container, observe whether SDK throws / errors / silently starts fresh — record result in specs/v1/memory-session.md open item 1

- rename product: cheerleader → evangelist, evangelist → support
- v3: HTTP request scrubbing (strip secrets from outbound agent HTTP calls)

## v2 channels

- email channel (IMAP + SMTP) — specs/v1/email.md
- reddit channel (DMs via snoowrap) — specs/v2/reddit.md
- facebook channel (fca-unofficial) — specs/v2/facebook.md
- twitter channel (agent-twitter-client) — specs/v2/twitter.md

## feed adapter (phase 1, all feed channels)

- synthetic inbound: dm / mention / timeline_post / reply_to_us event types
- outbound: reply / repost / react / post action types
- per-adapter watch config (accounts, keywords, subreddits)

## phase 2 (defer)

- MCP tools for deep querying: browse threads, search, follow, trending
- bus question: study HTTP proxying + MCP HTTP vs message bus before speccing
