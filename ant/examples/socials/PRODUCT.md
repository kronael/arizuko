name    = "socials"
brand   = "phosphene"
tagline = "Social presence manager — post, monitor, cross-post."
skills  = ["diary", "facts", "recall-memories", "web"]

# Operator setup
#
# 1. Populate facts/channels.md with platform accounts, audience notes
# 2. Populate facts/voice.md with brand voice, tone, do/don't list
# 3. Set TELEGRAM_BOT_TOKEN or DISCORD_TOKEN for operator approval
# 4. Configure social adapters (BLUESKY_*, MASTODON_*, DISCORD_*, etc.)
# 5. arizuko run <instance>

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Operator approval channel (Telegram or Discord — set at least one)"

[[env]]
key      = "DISCORD_BOT_TOKEN"
required = false
hint     = "Discord operator channel for post approval"

[[env]]
key      = "BLUESKY_IDENTIFIER"
required = false
hint     = "Bluesky handle (also set BLUESKY_APP_PASSWORD)"

[[env]]
key      = "MASTODON_SERVER"
required = false
hint     = "Mastodon instance URL (also set MASTODON_ACCESS_TOKEN)"
