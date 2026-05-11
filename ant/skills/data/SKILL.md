---
name: data
description: >
  Scraper, data collector, ETL pipeline patterns — asyncio, state.json,
  LeakyBucket rate limiting, dedup, backfill. USE for "build a scraper",
  "write an ETL pipeline", real-time feeds, asyncio pipelines,
  state.json, rate-limited API integrations. NOT for one-off scripts
  (use python/typescript) or download-and-transcribe (use acquire).
user-invocable: true
---

# Data collection

## Architecture

- asyncio-based, one loop hosts multiple collectors
- Sources yield items via async iterators
- Test against real API after ~10 lines — mental models are always wrong

## State

- `state.json` for recovery: resume from `last_processed + 1`
- Save every ~10k items, keep it portable JSON

## Sources

- RSS: feedparser
- REST: JsonSource base, save real responses as test fixtures
- WebSocket: restart-on-failure decorator
- RPC/Blockchain: parallel fetching, gRPC when available

## Error handling

- Exponential backoff, only retry transient errors
- Cache-first, RPC/API fallback
- LeakyBucket for paid APIs

## Backfill

- ALWAYS store raw data (compressed JSON/XZ) — NEVER delete history
- Incremental snapshots, CLI flag for start point

## Dedup

- URL or primary key (DB insert check)
- Vector similarity for semantic dedup
- NEVER trust upstream deduplication

## Concurrency

- Parallelize independent tasks
- Use DB constraints for dedup, not in-memory locks
