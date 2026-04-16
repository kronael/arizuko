---
name: service
description: >
  Use when writing REST APIs or web services. Covers /health,
  versioned routes, and input validation.
---

# Service/API

- `/health` = process alive, `/ready` = deps ready
- Versioned paths: `/v1/`, `/v2/` (not query params)
- Fail fast on missing data (404); fall back to last known data when current
  source is unavailable
