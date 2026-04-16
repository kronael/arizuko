---
name: ops
description: >
  Use when writing Dockerfiles, CI pipelines, or deployment config.
  Covers monitoring, logging, and systemd.
---

# Ops

## Docker

- ALWAYS pin image versions (NEVER `:latest`)
- Multi-stage if intermediate layers >100MB
- Layer order: base + system deps → lang deps → fetch → copy source → build
- Memory limit (2GB typical), build timeout (30m)

## Logging

- Format: `Mon DD HH:MM:SS.fff [LEVEL] message key=value`
- Log rotation via logrotate (not in app)

## Monitoring

- `/.well-known/live` for liveness, `/metrics` for Prometheus
- Prometheus labels: bounded enums only, NEVER unbounded values

## Error handling

- Exponential backoff (100ms…1600ms), only retry transient errors
- Alert after >10 persistent failures

## CI/CD

- Explicit make targets: `make build`, `make image`, `make test`
- Never mix debug/release artifacts locally
