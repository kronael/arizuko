name    = "github"
brand   = "atlas"
tagline = "Personal repo worker — triages issues, reviews PRs, opens PRs. Merges only when you say so."
skills  = ["diary", "recall-memories", "users", "issues", "oracle", "web"]

# A solo developer's agent for their own repos. It watches the repo, answers in
# the PR or issue thread it was addressed in, and acts back through the GitHub
# tools — never through a channel verb, so every repo-changing act passes the
# hold gate.
#
# Operator setup
#
# 1. Seed the group on a running instance:
#      arizuko group <inst> add web:dev dev --product github
# 2. Enable gitd through its compose fragment and manifest:
#      arizuko packages <inst> add gitd
#    Set WEB_HOST and GITHUB_TOKEN in <data-dir>/.env. gitd posts comments as
#    the token's user. Use a token with access to the watched repositories.
# 3. Add template/connectors/github.toml's [[connector]] block to
#    <data-dir>/connectors.toml once. The hosted MCP server supplies repo tools.
#    Store its token separately for the broker:
#      arizuko secret <inst> set folder:dev GITHUB_TOKEN --value <token>
#    Grant each required tool as mcp:github_<tool>:
#      arizuko group <inst> grant folder:dev dev "mcp:github_<tool>"
# 4. Hold merge calls for approval. A hold does not grant tool access:
#      arizuko group <inst> grant folder:dev dev "hold:mcp:github_merge_pull_request"
# 5. Register the webhook source:
#      arizuko github <inst> add repo me/proj dev
#    Paste the printed URL and secret into the repo's Settings > Webhooks.
#    Set application/json and select the events the agent should receive.
#    The secret is sealed in routd's store. Registration seeds github: routes:
#    PR mentions, direct replies and requests trigger turns. Assign the token's
#    user to each PR to watch its state and completed CI. Plain comments and
#    issue-only events go to dev#observe. Select check_run, check_suite and
#    workflow_run events for CI; the token needs Pull requests read access.
#    Within one repository, each PR or issue uses its own topic, pr/<n> or issue/<n>.
#    For an installed GitHub App, use add app <slug> dev and paste the URL
#    and secret into the App's webhook settings. Installation-token issuance
#    and refresh are not implemented; outbound comments still use GITHUB_TOKEN.
# 6. Restart the instance:
#      sudo systemctl restart arizuko_<inst>
#
# Approvals reach you two ways with no chat platform running: `/approve <id>`
# wherever you talk to it, and the link the held notice carries to
# /dash/approvals/. See specs/6/26 for the personal-mode topology.
#
# facts/repo.md is the minimum. Without it the agent reads the code fresh every
# turn and re-derives what you already know.

[[env]]
key      = "GITHUB_TOKEN"
required = true
hint     = "Enables gitd comments as the token's user; also store a folder-scoped token for the GitHub connector"

[[env]]
key      = "WEB_HOST"
required = true
hint     = "Public HTTPS host for GitHub webhook delivery and approval links"
