---
name: service
description: >
  Use when building REST APIs or web services. Covers /health,
  versioned routes, caching, and input validation.
---

# Service/API

- Liveness: /health (process alive), Readiness: /ready (deps ready)
- Versioned paths: /v1/, /v2/ (not query params)
- Fail fast on missing data (404), use last available data when current unavailable
