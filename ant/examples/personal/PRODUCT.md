name    = "personal"
brand   = "fiu"
tagline = "Personal assistant with persistent memory — knows you across every session."
skills  = ["diary", "facts", "recall-memories", "compact-memories", "users", "web", "oracle"]

# Operator setup
#
# 1. Populate facts/preferences.md with the person's context before deploy
# 2. Set TELEGRAM_BOT_TOKEN (primary channel)
# 3. arizuko run <instance>

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "BotFather token — primary personal channel"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional — enables web search and oracle research tasks"
