name    = "reality"
brand   = "rhias"
tagline = "Reality thread holder — ongoing life context across weeks and months."
skills  = ["diary", "facts", "recall-memories", "compact-memories", "users", "web", "oracle"]

# Operator setup
#
# 1. Seed facts/threads/ with one file per active ongoing situation
# 2. Set TELEGRAM_BOT_TOKEN (primary personal channel)
# 3. arizuko run <instance>

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "Primary personal channel — ongoing conversations"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional — for grounding context in external info and longer synthesis"
