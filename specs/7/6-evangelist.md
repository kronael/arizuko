# Evangelist

**Status**: planned

Community engagement agent. Watches external content sources
(reddit first, then twitter/discord/forums), scores relevance,
drafts responses, routes to human review before posting.

## What it does

1. **Scrape** — poll configured sources for new posts/threads
2. **Score** — classify relevance (problem fit, feature match)
3. **Draft** — write engagement response for high-relevance
4. **Review** — route to human via dashboard before posting
5. **Post** — approved drafts posted back to source platform
6. **Track** — dedup seen posts, log engagement history

## Architecture

```
sources (reddit API, RSS, discord webhooks)
       | scheduled poll (cron task via timed)
evangelist group (isolated arizuko agent)
       | scores + drafts
review queue (SQLite)
       | human approves via dashboard
post back (reddit API, etc.)
```

Evangelist runs as a dedicated arizuko group with its own
CLAUDE.md, facts, and memory. Cron task polls sources.
Agent processes new content, writes drafts to DB. Dashboard
shows queue for human review.

## Product config

Evangelist is a group with specific config:

```
groups/evangelist/
  CLAUDE.md    — persona, engagement rules, product knowledge
  facts/       — product features, talking points, competitors
  diary/       — engagement log
```

## Reddit source (v1)

### Polling

```toml
# services/evangelist.toml
[evangelist]
subreddits = ["r/claudeai", "r/LocalLLaMA", "r/selfhosted"]
poll_cron = "*/15 * * * *"
relevance_threshold = 6
```

Poll new posts and comments from configured subreddits since
last seen timestamp.

### Relevance scoring

Agent receives batch of new posts with product context from
facts/. Scores each 1-10 on:

- Problem fit (user has problem our product solves)
- Feature match (discussion about capability we have)
- Community fit (tone appropriate for engagement)

Below threshold: logged and skipped. Above: draft response.

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
  "status": "pending"
}
```

Strategy types: `helpful_reply`, `feature_mention`,
`experience_share`, `skip`.

## Review dashboard

```
/dash/evangelist/                        — draft queue
/dash/evangelist/api/drafts              — list
/dash/evangelist/api/drafts/:id/approve  — approve
/dash/evangelist/api/drafts/:id/reject   — reject
```

Rejection reason fed back to agent memory for learning.

## Draft storage

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

- Never lie about what the product does
- Never disparage competitors
- Be helpful first, promotional second
- Match community tone
- Quality over quantity
- Disclose affiliation when asked
- Skip negative sentiment threads
- No astroturfing — one account, transparent identity

## Implementation order

1. Reddit subreddit polling (feed adapter)
2. Relevance scoring + draft generation (agent prompt)
3. Draft storage + review dashboard (SQLite + static HTML)
4. Posting flow (gateway action + approval check)

## Future sources

| Source  | Mechanism       | Priority |
| ------- | --------------- | -------- |
| Reddit  | API polling     | v1       |
| Twitter | API v2 search   | v2       |
| Discord | webhook/bot     | v2       |
| HN      | algolia API/RSS | v3       |
| Forums  | RSS/scraping    | v3       |

Each source is a feed adapter — same interface (poll -> posts).

## Out of scope (v1)

- Auto-posting (all drafts require human review)
- Learning from approvals (manual CLAUDE.md tuning)
- Multi-account posting
- Cheerleader (inbound curation) — separate concern
