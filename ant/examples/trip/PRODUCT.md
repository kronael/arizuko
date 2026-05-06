name    = "trip"
brand   = "may"
tagline = "Trip planner — researches destinations, builds a day-by-day itinerary."
skills  = ["diary", "facts", "recall-memories", "web", "oracle", "find"]

[[env]]
key      = "OPENAI_API_KEY"
required = true
hint     = "Required for oracle multi-step research"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = false
hint     = "Primary channel"
