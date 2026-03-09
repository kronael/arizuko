# Evangelist

**Status**: open

Community engagement agent. Watches external content sources
(reddit first, then twitter/discord/forums), makes sense of
conversations, and suggests reactions — shilling project
features when appropriate, helping when genuine, silent when
not relevant.

Supersedes `specs/5/0-evangelist.md` and `specs/6/0-cheerleader.md`
which are now design notes only.

## What it does

1. **Scrape** — poll configured sources for new posts/threads
2. **Score** — classify relevance (problem fit, feature match)
3. **Draft** — write engagement response for high-relevance posts
4. **Review** — route to human via dashboard before posting
5. **Post** — approved drafts posted back to source platform
6. **Track** — dedup seen posts, log engagement history

## Architecture

```
sources (reddit API, RSS, discord webhooks)
       ↓ scheduled poll (kanipi cron task)
evangelist group (isolated kanipi agent)
       ↓ scores + drafts
review queue (SQLite)
       ↓ human approves via dash
post back (reddit API, etc.)
```

Evangelist runs as a dedicated kanipi group with its own
CLAUDE.md, facts, and memory. Scheduled task polls sources
on cron. Agent processes new content, writes drafts to DB.
Dashboard shows queue for human review.

## Product config

Evangelist is a kanipi product — a group with specific config:

```
groups/evangelist/
  CLAUDE.md          — persona, engagement rules, product knowledge
  facts/             — product features, talking points, competitors
  diary/             — engagement log
  drafts/            — pending drafts (or SQLite, see below)
```

## Reddit source (v1)

Reddit ships first. Uses the reddit channel spec (`3/G-reddit.md`)
for API access and auth. Evangelist adds subreddit monitoring
on top of the DM channel.

### Polling

```env
EVANGELIST_SUBREDDITS=r/claudeai,r/LocalLLaMA,r/selfhosted
EVANGELIST_POLL_CRON=*/15 * * * *    # every 15 min
EVANGELIST_RELEVANCE_THRESHOLD=6     # 1-10, skip below
```

Poll via snoowrap: fetch new posts and comments from configured
subreddits since last seen timestamp. Store raw post data for
agent context.

### Relevance scoring

Agent receives batch of new posts with product context from
facts/. Scores each 1-10 on:

- Problem fit (user has problem our product solves)
- Feature match (discussion about capability we have)
- Community fit (tone and context appropriate for engagement)

Posts below threshold are logged and skipped. Above threshold
get a draft response.

### Draft format

```json
{
  "id": "draft-abc123",
  "source": "reddit",
  "source_url": "https://reddit.com/r/...",
  "post_title": "...",
  "post_excerpt": "...",
  "relevance_score": 8,
  "draft_text": "...",
  "strategy": "helpful_reply",
  "created_at": "2026-03-09T12:00:00Z",
  "status": "pending"
}
```

Strategy types:

- `helpful_reply` — answer a question, link to docs
- `feature_mention` — relevant feature exists, mention naturally
- `experience_share` — share usage experience (conversational)
- `skip` — scored but not worth engaging

### Posting

Approved drafts posted via reddit API (snoowrap). Agent calls
a gateway action to post the comment, passing the draft ID.
Gateway verifies draft was approved before posting.

## Review dashboard

Part of the dashboards system (`specs/3/4-dashboards.md`).
Separate spec if it grows complex, but v1 is simple:

```
/dash/evangelist/           → draft review queue
/dash/evangelist/api/drafts → list pending/approved/rejected
/dash/evangelist/api/drafts/:id/approve  → approve (optional edit)
/dash/evangelist/api/drafts/:id/reject   → reject with reason
```

Shows: source URL, post excerpt, relevance score, draft text,
approve/edit/reject buttons. Rejection reason fed back to
agent memory for learning.

## Draft storage

SQLite table in the group's store:

```sql
CREATE TABLE evangelist_drafts (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  source_url TEXT NOT NULL,
  post_id TEXT NOT NULL,
  post_title TEXT,
  post_excerpt TEXT,
  relevance_score INTEGER,
  strategy TEXT,
  draft_text TEXT NOT NULL,
  edited_text TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  rejection_reason TEXT,
  created_at TEXT NOT NULL,
  reviewed_at TEXT,
  posted_at TEXT
);

CREATE TABLE evangelist_seen (
  post_id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  seen_at TEXT NOT NULL,
  engaged INTEGER NOT NULL DEFAULT 0
);
```

## Engagement rules (CLAUDE.md)

The agent's CLAUDE.md encodes engagement principles:

- Never lie about what the product does
- Never disparage competitors
- Be genuinely helpful first, promotional second
- Match community tone (technical in r/selfhosted, casual elsewhere)
- Don't engage in every thread — quality over quantity
- Disclose affiliation when directly asked
- Skip threads with strong negative sentiment toward product
- No astroturfing — one account, transparent identity

## Gateway changes

1. Reddit channel (`3/G-reddit.md`) provides API access
2. New gateway action: `post_reddit_comment` (checks draft approved)
3. Cron task entry for evangelist polling
4. Dashboard routes for draft review

## Implementation order

1. Reddit channel (DMs) — ships independently
2. Reddit subreddit polling — evangelist-specific extension
3. Relevance scoring + draft generation — agent prompt engineering
4. Draft storage + review dashboard — SQLite + static HTML
5. Posting flow — gateway action + approval check

## Future sources

| Source  | Mechanism          | Priority |
| ------- | ------------------ | -------- |
| Reddit  | snoowrap polling   | v1       |
| Twitter | API v2 search      | v2       |
| Discord | webhook / bot join | v2       |
| HN      | algolia API / RSS  | v3       |
| Forums  | RSS / scraping     | v3       |

Each source is a feed adapter — same interface (poll → posts),
different API. Agent doesn't care where content comes from.

## Out of scope (v1)

- Auto-posting (all drafts require human review)
- Learning from approvals (manual CLAUDE.md tuning for now)
- Multi-account posting (one reddit account per instance)
- Sentiment analysis beyond agent judgement
- Cheerleader (inbound channel curation) — separate concern
