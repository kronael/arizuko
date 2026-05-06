name    = "creator"
brand   = "inari"
tagline = "Content creation pipeline — draft, refine, publish."
skills  = ["diary", "facts", "recall-memories", "web", "oracle", "find"]

# Operator setup
#
# 1. Populate facts/style.md with brand voice, tone, do/don't rules
# 2. Set TELEGRAM_BOT_TOKEN for the operator approval workflow
# 3. Set CODEX_API_KEY — oracle is required for multi-source drafting
# 4. Configure at least one publish adapter (bskyd or mastd)
# 5. arizuko run <instance>

[[env]]
key      = "CODEX_API_KEY"
required = true
hint     = "Oracle — required for multi-source synthesis and drafting"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Operator approval workflow (pitch → approve → draft → publish)"

[[env]]
key      = "BLUESKY_IDENTIFIER"
required = false
hint     = "Bluesky handle for publishing (also set BLUESKY_APP_PASSWORD)"
