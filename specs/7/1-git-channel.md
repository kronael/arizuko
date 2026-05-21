---
status: draft
---

# Git as channel — gitd adapter

Treat a Git host (GitHub primarily; GitLab/Gitea as the same shape) as
just another channel. Each repo is a folder; each PR, issue, comment,
push, review, CI status, and release is a message on the bus. The
agent reads the repo via davd, replies via the platform API, and
accumulates per-repo facts/diary/users like any other channel.

Inspired by Dosu (GitHub bot that learns per-repo). The pattern that
fits arizuko: the channel's natural unit (repo) is already the agent's
natural unit (folder). No impedance layer.

## Why this is a clean fit

- One repo == one folder. PERSONA per repo, MEMORY per repo,
  facts/ per repo, contributors live in users/.
- The agent already has a workspace (davd). The repo IS the workspace.
  `git pull` on turn entry; commits become outbound actions.
- Git host events map naturally onto chanlib verbs: `comment` ≈ `reply`,
  `commit` ≈ `post`, `react` ≈ `like`, `merge` ≈ a higher-order action.
- Long-lived discussions (PR threads, issue threads) are the same shape
  as Slack threads — `reply_to_id` is the comment/PR/issue id.

## Architecture

Mirrors `slakd` shape: Go daemon, HTTP server, registers with router,
receives webhooks from GitHub, exposes outbound `/send`, `/reply`,
`/comment`, `/review`, etc.

```
gitd/
  main.go              entrypoint, env, lifecycle
  github.go            go-github client wrapper, auth, rate limits
  webhook.go           POST /webhook — GitHub event ingest, HMAC verify
  outbound.go          /send /reply /comment /review /label /close
  client.go            router registration
```

Compose template: `template/services/gitd.toml`. No edits to
`proxyd/main.go` or `compose/compose.go` (per the standalone-proxyd
contract, spec `5/35`).

## JID format

Prefix `git:`. Hierarchical under the repo.

| Surface                     | JID                                          |
| --------------------------- | -------------------------------------------- |
| Repo root (default channel) | `git:<owner>/<repo>`                         |
| PR thread                   | `git:<owner>/<repo>/pr/<number>`             |
| Issue thread                | `git:<owner>/<repo>/issue/<number>`          |
| Discussion                  | `git:<owner>/<repo>/disc/<number>`           |
| Commit (as message target)  | `git:<owner>/<repo>/commit/<sha>`            |
| Review thread on PR         | `git:<owner>/<repo>/pr/<number>/review/<id>` |

Group routes bind on the prefix:
`git:kronael/arizuko/* → arizuko-self` (one agent watches the repo).
Per-PR sticky routing falls out of the existing reply-routing rules.

## Events → verbs (inbound)

GitHub webhook event → message row.

| GitHub event                    | Verb        | Notes                                          |
| ------------------------------- | ----------- | ---------------------------------------------- |
| `issue_comment.created`         | `reply`     | If `in_reply_to_id` of a bot comment → mention |
| `pull_request_review_comment`   | `reply`     | Threaded review reply                          |
| `issues.opened`                 | `message`   | New issue → top-level                          |
| `pull_request.opened`           | `message`   | New PR → top-level                             |
| `pull_request.synchronize`      | `update`    | New commits on PR — observe only by default    |
| `push`                          | `commit`    | Branch push                                    |
| `release.published`             | `release`   |                                                |
| `workflow_run.completed`        | `ci_status` | CI pass/fail signal                            |
| `reaction.created`              | `like`      | Emoji map: 👍→like, 👀→viewed, etc.            |
| `pull_request_review.submitted` | `review`    | approve / request_changes / comment            |

Mention detection: GitHub `@bot-handle` in the body, plus
`in_reply_to` of any bot-authored comment (consistent with the
ring-buffer matcher pattern Slack/Discord use).

## Verbs (outbound)

Reuse the chanlib verb table. Capability cells:

| Verb        | Native | API                                   | Notes                            |
| ----------- | ------ | ------------------------------------- | -------------------------------- |
| `reply`     | yes    | `POST /repos/.../issues/.../comments` | Issue / PR thread                |
| `send`      | yes    | `POST /repos/.../issues`              | Open a fresh issue               |
| `post`      | yes    | git push (via davd workspace)         | Commit to a branch               |
| `like`      | yes    | `POST .../reactions`                  | Emoji reactions on comments      |
| `dislike`   | hint   | n/a                                   | No native downvote → 👎 reaction |
| `edit`      | yes    | `PATCH .../comments/<id>`             | Edit own comments                |
| `delete`    | yes    | `DELETE .../comments/<id>`            | Own comments only                |
| `review`    | native | `POST .../pulls/.../reviews`          | approve / request_changes        |
| `label`     | native | `POST .../issues/.../labels`          | Issue/PR labeling                |
| `close`     | native | `PATCH .../issues/...`                |                                  |
| `merge`     | native | `PUT .../pulls/.../merge`             | Gated behind grants              |
| `send_file` | yes    | upload via git push                   | Workspace write + commit         |

`review`, `label`, `close`, `merge` are Git-specific verbs — extend
the chanlib verb registry, advertised via `/caps`.

## Auth

Two paths:

1. **GitHub App (recommended)** — per-install token, fine-grained repo
   permissions, webhook delivery built in. `GITHUB_APP_ID`,
   `GITHUB_APP_PRIVATE_KEY`, `GITHUB_INSTALLATION_ID`.
2. **Personal Access Token (fallback)** — `GITHUB_TOKEN`, scoped
   classic or fine-grained PAT. Operator must wire webhooks manually
   (org/repo webhook → `https://<host>/git/webhook`).

App auth is mandatory for `merge` and `review` actions — PATs leak
the user identity into the audit log; GitHub Apps appear as
`<app>[bot]`.

## Workspace integration (davd)

The repo IS the agent's workspace. On group setup:

```
git clone <repo> /srv/data/arizuko_<inst>/groups/<folder>/workspace/
```

davd already mounts the workspace into the container as a writable
volume. Agent runs `git`, `make`, `npm test` directly. Outbound
`post` (commit) = stage + commit + push from inside the container,
using the GitHub App installation token for auth (short-lived,
re-issued per turn).

This means **the repo is both the channel AND the agent's tools**.
That's the Dosu insight collapsed into arizuko's existing primitives.

## Per-repo persistence (Dosu pattern)

Each `git:<owner>/<repo>` folder accumulates:

- **facts/** — codebase facts: where the auth code lives, which test
  command works, which directories are generated, the ADR registry.
  Filled by the `/facts` skill researching from the repo itself.
- **diary/** — chronological work log per repo. Decision history,
  bug patterns, deploy notes.
- **users/** — contributor profiles. Filled from PR review patterns,
  commit authorship, comment style. Maps GitHub handle → identity.
- **PERSONA.md** — repo-specific voice (matches project style).

A new repo onboarding flow runs `/facts repo` and `/users` against
the contributor list before the first turn.

## Honest gaps

- **CI feedback loop**: the agent can read CI status but cannot
  unstuck a broken pipeline without scoped grants (re-run workflow,
  edit YAML). Default-deny.
- **Force-push and history rewriting**: never allowed without HITL
  firewall (`specs/7/4`). The agent can `commit` and `push` to its
  branches; cannot `push --force` or `rebase` shared branches.
- **Secrets in CI**: agent must never read `GITHUB_TOKEN`-equivalent
  secrets from `actions/secrets`. Workspace is read-only on
  `.github/workflows/` unless grant explicitly opens it.
- **Org-level events** (members joined, repo created): out of scope
  for v1. Per-repo only.
- **Polling fallback**: webhooks require a public endpoint. If the
  instance is behind NAT, fall back to polling — strictly worse but
  unblocks dev. `GITHUB_POLL_INTERVAL` cursor on issue/PR list.

## Out of scope (initial ship)

- GitLab / Gitea / Bitbucket — same shape, separate daemons later.
- Code search across repos (use external search, agent grepts in
  workspace).
- PR diff comments at line granularity (review comments are top-level
  in v1; line-anchored is v2).
- Notifications API (`/notifications`) — webhook coverage is
  sufficient.
- Cross-repo coordination (multi-repo PRs) — each repo is its own
  folder; coordinate at the message-bus layer via verb=mention to a
  sibling.

## Compose & env

```
GITHUB_APP_ID=
GITHUB_APP_PRIVATE_KEY=/srv/data/store/github-auth/private-key.pem
GITHUB_INSTALLATION_ID=
GITHUB_WEBHOOK_SECRET=
GITHUB_TOKEN=                # fallback if no App
GITHUB_POLL_INTERVAL=120     # only if no webhook
GITD_PORT=8080
```

Webhook URL: `https://<host>/git/webhook`. proxyd route in the
service TOML.

## Effort estimate

~1500-2500 LOC Go. go-github + golang.org/x/oauth2 for auth.
Comparable to slakd. Multi-source ingest (webhooks + poll
fallback) drives most of the test surface.

## Risks

- **Webhook delivery loss**: GitHub retries 8x then drops. Defense
  is a periodic reconcile poll (every 5 min) that diffs known issue
  state against the API — slow but bounded.
- **Rate limits**: 5000 req/hr per App install, 15000 for App-level.
  Should be ample for one repo; bursty if the agent reacts to every
  comment. Token-bucket in the outbound path.
- **Bot loops**: agent comments trigger `issue_comment.created`.
  Filter on `sender.type == "Bot"` + own App slug.
- **PR comment spam**: agent must rate-limit replies in the same
  thread (existing engagement-TTL pattern applies).
- **Grant explosion**: per-repo grants matter. `merge` to main is
  not the same trust as `comment`. Lattice:
  `comment < label < close < review < merge`.

## Why this matters

Three things fall out for free:

1. **The "company brain" use case finally has teeth.** A repo-scoped
   agent that watches issues + PRs + commits is closer to "agent
   that knows the codebase" than any vector-search RAG.
2. **The auto-migrate skill becomes self-hosting.** The agent
   watching arizuko itself can open PRs against arizuko.
3. **Product extension**: a "code-review" product (spec slot open)
   slots into the existing product template — `arizuko create
<name> --product code-review` gives you a repo-bound agent with
   review + comment + label grants.

## Decisions

- JID prefix is `git:` (host-neutral). Future GitLab/Gitea adapters
  reuse the prefix; route on the host part of the path:
  `git:gitlab.com/<owner>/<repo>`. Default is `github.com`, elided
  for brevity.
- GitHub App over PAT — audit trail clarity and per-repo scoping.
- Repo IS workspace — no separate "code context" abstraction.
- `merge`/`review`/`label`/`close` extend chanlib verbs rather than
  shoehorning into generic primitives.
- HITL firewall required for `merge` and `push --force` by default.
