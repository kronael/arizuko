name    = "slack-team"
brand   = "slack-team"
tagline = "One agent in your team's Slack — channel persona, per-teammate memory, email ingest."
skills  = ["diary", "facts", "recall-memories", "users", "issues", "web", "dispatch", "resolve", "find", "oracle"]

# Operator setup
#
# 1. Install the Slack App (scopes: chat:write, channels:history, im:history,
#    groups:history, assistant:write, users:read). See setup.html step 3.
# 2. Set SLACK_BOT_TOKEN + SLACK_SIGNING_SECRET in .env.
# 3. For email ingest (data source for the agent): set EMAIL_* vars and
#    drop template/services/emaid.toml into <data-dir>/services/.
# 4. arizuko run <instance>
# 5. Invite the bot to a channel. mcpc add_route to scope per-channel
#    behavior (mention-only triggers + observe catch-all). See setup.html
#    step 9 + the autoviv concept.

[[env]]
key      = "SLACK_BOT_TOKEN"
required = true
hint     = "xoxb-... from your Slack App's OAuth & Permissions page"

[[env]]
key      = "SLACK_SIGNING_SECRET"
required = true
hint     = "from Slack App's Basic Information page"

[[env]]
key      = "EMAIL_ACCOUNT"
required = false
hint     = "Optional: shared inbox the agent reads (e.g. atlas@yourdomain.com). Enables email-as-data-ingest."

[[env]]
key      = "EMAIL_PASSWORD"
required = false
hint     = "App password for EMAIL_ACCOUNT. Gmail requires app-specific passwords (Google account > Security > 2FA > App passwords)."

[[env]]
key      = "EMAIL_IMAP_HOST"
required = false
hint     = "e.g. imap.gmail.com"

[[env]]
key      = "EMAIL_SMTP_HOST"
required = false
hint     = "e.g. smtp.gmail.com"

[[env]]
key      = "ANTHROPIC_API_KEY"
required = true
hint     = "for the in-container Claude Code agent"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional: enables /oracle skill for second-opinion analysis"

[[env]]
key      = "GITHUB_CLIENT_ID"
required = false
hint     = "Optional: OAuth so teammates can link their GitHub identity (per-user memory across surfaces)"

[[env]]
key      = "GITHUB_CLIENT_SECRET"
required = false
hint     = "paired with GITHUB_CLIENT_ID"
