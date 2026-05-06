name    = "creator"
brand   = "inari"
tagline = "Content creation pipeline — research, draft, refine, publish on approval."
skills  = ["diary", "facts", "recall-memories", "web", "oracle"]

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Enables oracle for deeper research"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Operator approval channel"
