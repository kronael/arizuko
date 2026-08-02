---
status: draft
---

# Git as channel — gitd adapter

Treat a Git host (GitHub first; GitLab/Gitea are the same shape) as another
channel. Each repo is a folder; each PR, issue, comment, push, review, CI
status, and release is a message on the bus.

Inspired by Dosu (a GitHub bot that learns per-repo). The reason it fits:
**the channel's natural unit (repo) is already the agent's natural unit
(folder)** — no impedance layer. Long-lived PR and issue threads are the
same shape as Slack threads, so `reply_to_id` is just the comment id.

## Decisions

- **JID prefix is `git:`**, host-neutral: `git:<owner>/<repo>` for the repo
  root, `git:<owner>/<repo>/pr/<n>` and `.../issue/<n>` for threads. Future
  GitLab/Gitea adapters reuse the prefix and route on the host part
  (`git:gitlab.com/<owner>/<repo>`); `github.com` is the default and elided.
  Per-PR sticky routing then falls out of the existing reply-routing rules.
- **GitHub App over PAT.** A PAT leaks the granting user's identity into
  every audit trail; an App appears as `<app>[bot]` and scopes per repo.
  App auth is mandatory for `merge` and `review`. PAT stays as a fallback
  for operators who wire webhooks by hand.
- **The repo IS the workspace** — no separate "code context" abstraction.
  davd already mounts the group's workspace writable, so `git clone` into it
  makes the agent's tools and the channel the same surface. Outbound
  `post` = stage + commit + push from inside the container using the App's
  short-lived installation token. This is the Dosu insight collapsed into
  primitives arizuko already has.
- **`review`/`label`/`close`/`merge` extend the chanlib verb registry**
  rather than being shoehorned into generic primitives, and are advertised
  via `/caps`. They are genuinely different acts, not modes of `reply`.
- **HITL required for `merge` and `push --force`.** Grant trust is a
  lattice — `comment < label < close < review < merge` — and the top of it
  is not something a persona instruction should be holding
  ([5/19-hitl-firewall](../5/19-hitl-firewall.md)).

## Architecture

Mirrors `slakd`: a Go daemon that registers with routd, ingests GitHub
webhooks (HMAC-verified), and exposes outbound send/reply/comment/review.
Ships `template/services/gitd.yml` — a partial compose file docker
`include`s verbatim — plus a routes JSON for its inbound webhook path. No
edit to `proxyd/main.go` or `compose/compose.go` (spec
[5/7-proxyd-standalone](../5/7-proxyd-standalone.md)).

Inbound events map onto chanlib verbs: `issue_comment.created` and
`pull_request_review_comment` → `reply`; `issues.opened` /
`pull_request.opened` → `message`; `push` → `commit`;
`workflow_run.completed` → `ci_status`; `reaction.created` → `like`.
Mention detection is `@bot-handle` in the body plus `in_reply_to` of any
bot-authored comment — the same ring-buffer matcher Slack and Discord use.

## Per-repo persistence

Each `git:<owner>/<repo>` folder accumulates `facts/` (where the auth code
lives, which test command works, what is generated), `diary/` (decisions,
bug patterns, deploy notes), `users/` (contributor profiles keyed to GitHub
handles), and a repo-specific `PERSONA.md`. Onboarding a new repo runs the
facts and users skills against the repo and its contributor list before the
first turn.

## Honest gaps

- **Bot loops.** The agent's own comment fires `issue_comment.created`.
  Filter on `sender.type == "Bot"` plus the App slug, and rate-limit replies
  within one thread (the existing engagement-TTL pattern).
- **Webhook delivery loss.** GitHub retries 8× then drops. Defense is a
  periodic reconcile poll that diffs known issue state against the API —
  slow but bounded. Behind NAT with no public endpoint, polling is the only
  ingest; strictly worse, but it unblocks dev.
- **CI feedback loop.** The agent can read CI status but cannot unstick a
  broken pipeline without scoped grants (re-run workflow, edit YAML).
  Default-deny, and `.github/workflows/` stays read-only unless a grant
  explicitly opens it.
- **Rate limits.** 5000 req/hr per App install is ample for one repo but
  bursty if the agent reacts to every comment — token-bucket the outbound
  path.

## Out of scope (initial ship)

- GitLab / Gitea / Bitbucket — same shape, separate daemons later.
- Line-anchored PR review comments — top-level review comments in v1.
- Org-level events (member joined, repo created) — per-repo only.
- Cross-repo coordination — each repo is its own folder; coordinate at the
  message bus via a mention to a sibling.

## Why this matters

A repo-scoped agent watching issues, PRs, and commits is closer to "an agent
that knows the codebase" than vector-search RAG is — it makes the
company-brain use case ([8-company-brain](8-company-brain.md)) concrete. It
also makes the self-migration loop self-hosting: the agent watching arizuko
can open PRs against arizuko.
