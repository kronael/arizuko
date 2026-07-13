name    = "aws-devops"
brand   = "argus"
tagline = "An SRE agent in your team's chat — reads your runbooks, runs read-before-write, and each engineer acts with their own AWS keys."
skills  = ["diary", "recall-memories", "users", "issues", "find", "bash", "commit", "web", "oracle", "resolve"]

# Operator setup
#
# AWS credentials are NOT .env vars — they live in the secrets table so
# each engineer can carry their own. The [[env]] blocks below cover only
# platform anchors (model key, chat adapters, web host). AWS wiring is
# steps 3–6 here.
#
# 1. Build the binary + agent image (make build; sudo make images; sudo make agent).
# 2. Set the .env keys below (ANTHROPIC_API_KEY, TELEGRAM_BOT_TOKEN, WEB_HOST).
# 3. Open AWS egress for the group. One entry covers every regional
#    endpoint — crackbox matches subdomains (crackbox/pkg/match/match.go):
#      arizuko network <instance> allow main amazonaws.com
#      arizuko network <instance> allow main pypi.org
#      arizuko network <instance> allow main files.pythonhosted.org
#    (pypi hosts let the agent fetch awscli/boto3 via uvx; drop them if you
#    bake the AWS CLI into a custom agent image.)
# 4. Shared/team fallback role (folder-scoped secret — applies when a
#    caller has no keys of their own):
#      arizuko secret <instance> set main AWS_ACCESS_KEY_ID     --value AKIA...
#      arizuko secret <instance> set main AWS_SECRET_ACCESS_KEY --value ...
#      arizuko secret <instance> set main AWS_DEFAULT_REGION    --value us-east-1
# 5. Per-operator keys (BYOA). Each engineer's own keys shadow the folder
#    fallback for the turns THEY trigger — audited to them in CloudTrail:
#      arizuko user-secret <instance> set <user_sub> AWS_ACCESS_KEY_ID     --value AKIA...
#      arizuko user-secret <instance> set <user_sub> AWS_SECRET_ACCESS_KEY --value ...
#    Or hand each engineer /dash/me/secrets after they OAuth-link at /auth/login.
# 6. IAM caps the blast radius. Give read-first policies wide (Describe*,
#    List*, Get*) and destructive actions (Terminate*, Delete*, Stop*)
#    narrow or denied per key. arizuko attributes the identity; IAM decides
#    what that identity may do; CloudTrail records it per human.
# 7. arizuko run <instance>

[[env]]
key      = "ANTHROPIC_API_KEY"
required = true
hint     = "for the in-container Claude Code agent"

[[env]]
key      = "TELEGRAM_BOT_TOKEN"
required = true
hint     = "BotFather token — oncall phone channel (alerts + destructive-action confirmations land here)"

[[env]]
key      = "WEB_HOST"
required = true
hint     = "public hostname for /dash, /auth/login, and each engineer's /dash/me/secrets page"

[[env]]
key      = "SLACK_BOT_TOKEN"
required = false
hint     = "Optional: xoxb-... — a shared incident channel for the team alongside Telegram oncall"

[[env]]
key      = "SLACK_SIGNING_SECRET"
required = false
hint     = "paired with SLACK_BOT_TOKEN — Slack App > Basic Information"

[[env]]
key      = "DISCORD_BOT_TOKEN"
required = false
hint     = "Optional: ops-team Discord channel instead of (or alongside) Slack"

[[env]]
key      = "GITHUB_CLIENT_ID"
required = false
hint     = "Optional: OAuth so each engineer links their chat identity to a web login, then self-serves their own AWS keys at /dash/me/secrets"

[[env]]
key      = "GITHUB_CLIENT_SECRET"
required = false
hint     = "paired with GITHUB_CLIENT_ID"

[[env]]
key      = "OPENAI_API_KEY"
required = false
hint     = "Optional: enables the /oracle skill for a second opinion on a risky change"
