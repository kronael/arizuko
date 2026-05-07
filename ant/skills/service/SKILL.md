---
name: service
description: REST API and web service patterns — /health, versioned routes, input validation.
when_to_use: Use when writing REST APIs or web services.
---

# Service/API

- `/health` = process alive, `/ready` = deps ready
- Versioned paths: `/v1/`, `/v2/` (not query params)
- Fail fast on missing data (404); fall back to last known data when current
  source is unavailable
