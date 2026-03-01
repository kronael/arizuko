# Cheerleader

Eliza-inspired social media curator. Watches inbound channels, suggests
or drafts responses, and routes them through human review before posting.

## Problem

Maintaining consistent, on-brand presence across multiple chat channels
(Telegram groups, WhatsApp, Discord) is time-consuming. Humans miss
messages, respond inconsistently, or burn out on repetitive tasks.

## What it does

- Monitors all inbound channels for messages that warrant a response
- Drafts candidate replies using the Claude agent
- Surfaces drafts in a web dashboard for human review/edit/approve/reject
- Posts approved replies back to the originating channel via IPC
- Learns from approvals/rejections to improve future drafts

## Architecture

```
channels → gateway → cheerleader-agent (kanipi main group)
                         ↓ drafts
                    web dashboard (vite)
                         ↓ approved
                    gateway → channel reply
```

The cheerleader runs as a kanipi agent in the main group. It receives
copies of all inbound messages (or a curated subset via trigger rules),
drafts responses, and writes them to a review queue in the SQLite DB.

The vite web app reads the review queue and shows pending drafts with
context. Approve → agent posts reply via `mcp__nanoclaw__send_message`.
Reject → draft discarded. Edit + approve → sends edited version.

## Review queue schema

```sql
CREATE TABLE drafts (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  channel TEXT NOT NULL,          -- telegram / whatsapp / discord
  chat_jid TEXT NOT NULL,
  source_message TEXT NOT NULL,   -- original message that triggered draft
  draft_text TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending / approved / rejected
  edited_text TEXT,               -- set if human edited before approving
  reviewed_at TEXT
);
```

## Config

```env
CHEERLEADER_ENABLED=1
CHEERLEADER_CHANNELS=telegram,discord   # which channels to monitor
CHEERLEADER_AUTO_APPROVE=0              # 1 = post without review (dangerous)
```

## Web dashboard routes

- `GET /drafts` — list pending drafts with source context
- `POST /drafts/:id/approve` — approve (with optional edited_text body)
- `POST /drafts/:id/reject` — reject

## V1 scope

- Single channel (Telegram) to reduce surface area
- No auto-approve — all drafts require human review
- Dashboard shows last 50 pending drafts
- No learning loop (approvals not fed back to agent)
