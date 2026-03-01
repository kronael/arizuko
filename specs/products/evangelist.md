# Evangelist

Outbound community engagement agent. Monitors external communities
(Reddit, Discord servers, forums) for relevant conversations and
participates with helpful, on-brand responses.

## Problem

Being present in external communities where potential users hang out
requires constant manual monitoring. Opportunities to help (and build
trust) are missed because no one is watching.

## What it does

- Polls configured external communities for new threads/posts
- Classifies posts by relevance (product fit, problem match)
- Drafts engagement responses for high-relevance posts
- Routes drafts to human review before posting
- Tracks which posts were engaged with to avoid duplicates

## Architecture

```
external communities (Reddit API, Discord webhooks, RSS)
         ↓ scheduled poll (cron task)
evangelist-agent (kanipi isolated context)
         ↓ drafts + relevance scores
web dashboard (shared with cheerleader review queue)
         ↓ approved
external API post (Reddit comment, Discord reply, etc.)
```

Runs as a scheduled kanipi task (cron, not reactive). Agent polls
sources, scores relevance, drafts responses, writes to review queue.
Approved drafts are posted by the agent via tool calls with stored
OAuth credentials.

## Config

```env
EVANGELIST_ENABLED=1
EVANGELIST_SOURCES=reddit:r/claudeai,discord:server-id/channel-id
EVANGELIST_POLL_CRON=0 * * * *   # hourly
EVANGELIST_RELEVANCE_THRESHOLD=7  # 1-10, skip below this
REDDIT_CLIENT_ID=...
REDDIT_CLIENT_SECRET=...
REDDIT_REFRESH_TOKEN=...
```

## Review queue

Shares draft schema with Cheerleader. `channel` field identifies
source platform. Dashboard shows source URL, post excerpt, relevance
score, and draft reply.

## V1 scope

- Reddit only (clearest API, highest signal communities)
- One configured subreddit to start
- Relevance scoring via agent (prompt includes product description)
- No auto-post — all drafts reviewed
- Deduplication: store engaged post IDs in SQLite

## Dedup schema

```sql
CREATE TABLE evangelist_seen (
  post_id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  seen_at TEXT NOT NULL,
  engaged INTEGER NOT NULL DEFAULT 0
);
```
