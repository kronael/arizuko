name    = "support"
brand   = "atlas"
tagline = "Embedded support agent — answers from your knowledge base, escalates when stuck."
skills  = ["diary", "facts", "recall-memories", "users", "issues", "web"]

# Operator setup
#
# 1. Copy SOUL.md and CLAUDE.md into your group folder
# 2. Populate facts/ with one markdown file per topic
#    OR place reference docs under refs/<product>/
# 3. Set TELEGRAM_BOT_TOKEN for a primary channel + escalation target
# 4. Embed the slink widget on your product site (webd)
# 5. arizuko run <instance>
#
# facts/ is the minimum — without it the agent can only web-search.

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "BotFather token — primary channel + escalation target for unanswered questions"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional — enables web search fallback when the knowledge base has no answer"
