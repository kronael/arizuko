name    = "pm"
brand   = "sloth"
tagline = "PM agent — task board, status summaries, decisions."
skills  = ["diary", "facts", "recall-memories", "users"]

# Operator setup
#
# 1. Populate facts/team.md with team members, roles, and contacts
# 2. Populate facts/tasks.md with the initial task board (or leave as seed)
# 3. Set TELEGRAM_BOT_TOKEN or DISCORD_TOKEN for the team channel
# 4. arizuko run <instance>

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Team channel (Telegram or Discord — set at least one)"

[[env]]
key      = "DISCORD_BOT_TOKEN"
required = false
hint     = "Discord team channel for task tracking and status updates"
