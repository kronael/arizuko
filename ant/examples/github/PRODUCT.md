name    = "github"
brand   = "atlas"
tagline = "Personal repo worker — triages issues, reviews PRs, opens PRs. Merges only when you say so."
skills  = ["diary", "recall-memories", "users", "issues", "oracle", "web"]

# A solo developer's agent for their own repos. It watches the repo, answers in
# the PR or issue thread it was addressed in, and acts back through the GitHub
# tools — never through a channel verb, so every repo-changing act passes the
# hold gate.
#
# REQUIRES gitd, which is specced (specs/6/27) and NOT YET BUILT. Until it
# ships, the egress half works on its own — the github connector gives the
# agent its gh tools and the hold gate covers them — but nothing delivers repo
# events, so steps 4 and 6 below have nothing to route.
#
# Operator setup
#
# 1. Install the GitHub App on the repos it should watch. An App, not a PAT:
#    a PAT stamps YOUR identity on every action it takes.
# 2. Set GITHUB_APP_ID + GITHUB_PRIVATE_KEY + GITHUB_WEBHOOK_SECRET.
# 3. Enable the github connector so the agent gets the gh tools:
#      cp template/connectors/github.toml <data-dir>/connectors/
# 4. Route the repo. One row per level — `**` is NOT a subtree glob and a `**`
#    row silently matches nothing (specs/6/27 § "no subtree glob"):
#      arizuko route <inst> add "chat_jid=git:me/proj/pr/* verb=reply"    dev
#      arizuko route <inst> add "chat_jid=git:me/proj/pr/* verb=message"  dev
#      arizuko route <inst> add "chat_jid=git:me/proj/pr/*"    dev#observe -s 100
#      arizuko route <inst> add "chat_jid=git:me/proj/issue/*" dev#observe -s 100
#      arizuko route <inst> add "chat_jid=git:me/proj"         dev#observe -s 100
# 5. Hold the acts you want to sign off. Merge is the one that matters:
#      arizuko group <inst> grant dev "hold:mcp:github_merge_pull_request" dev
#      arizuko group <inst> grant dev "hold:mcp:github_create_pull_request" dev
# 6. arizuko run <instance>
#
# Approvals reach you two ways with no chat platform running: `/approve <id>`
# wherever you talk to it, and the link the held notice carries to
# /dash/approvals/. See specs/6/26 for the personal-mode topology.
#
# facts/repo.md is the minimum. Without it the agent reads the code fresh every
# turn and re-derives what you already know.

[[env]]
key      = "GITHUB_APP_ID"
required = true
hint     = "GitHub App id — the agent posts as <app>[bot], not as you"

[[env]]
key      = "GITHUB_PRIVATE_KEY"
required = true
hint     = "App private key (PEM) — mints the short-lived installation token"

[[env]]
key      = "GITHUB_WEBHOOK_SECRET"
required = true
hint     = "Shared secret gitd verifies the inbound webhook HMAC against"

[[env]]
key      = "WEB_HOST"
required = false
hint     = "Set it and every held-call notice carries a link straight to the approval page"
