name    = "strategy"
brand   = "prometheus"
tagline = "Domain researcher — weekly briefings and deep-dive memos."
skills  = ["diary", "facts", "recall-memories", "web", "oracle"]

[[env]]
key      = "OPENAI_API_KEY"
required = true
hint     = "Required for oracle-powered research"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Primary channel for briefing delivery"
