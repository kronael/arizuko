name    = "trip"
brand   = "may"
tagline = "Trip planner — research, synthesise, structured plan."
skills  = ["diary", "facts", "recall-memories", "web", "oracle", "find"]

# Operator setup
#
# 1. Populate facts/preferences.md with traveller profile before deploy
# 2. Set TELEGRAM_BOT_TOKEN or WHATSAPP_TOKEN for personal channel
# 3. Set CODEX_API_KEY or OPENAI_API_KEY — oracle is critical for this product
# 4. arizuko run <instance>

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Primary personal channel (Telegram or WhatsApp — set at least one)"

[[env]]
key      = "CODEX_API_KEY"
required = true
hint     = "Oracle — required for multi-step destination research and synthesis"
