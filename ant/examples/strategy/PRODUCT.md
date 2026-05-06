name    = "strategy"
brand   = "prometheus"
tagline = "Domain researcher — weekly briefings and deep dives."
skills  = ["diary", "facts", "recall-memories", "web", "oracle", "find"]

# Operator setup
#
# 1. Populate facts/domain.md with the domain, key players, what matters
# 2. Populate facts/watchlist.md with sources, companies, topics to track
# 3. Set TELEGRAM_BOT_TOKEN or DISCORD_TOKEN for team channel
# 4. Set CODEX_API_KEY — oracle is critical for synthesis
# 5. arizuko run <instance>

[[env]]
key      = "CODEX_API_KEY"
required = true
hint     = "Oracle — required for deep document analysis and synthesis"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Team channel for briefing delivery (Telegram or Discord — set at least one)"

[[env]]
key      = "DISCORD_BOT_TOKEN"
required = false
hint     = "Discord team channel for briefing delivery"
