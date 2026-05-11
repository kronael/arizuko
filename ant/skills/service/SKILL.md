---
name: service
description: >
  REST API and web-service patterns — `/health`, `/ready`, versioned
  paths `/v1/`, validation before persistence. USE for "build a REST
  API", "/health endpoint", "add a /v1/ route", HTTP handlers,
  microservices, request validation. NOT for CLI tools (use cli) or
  static web pages (use web).
user-invocable: true
---

# Service/API

- `/health` = process alive, `/ready` = deps ready
- Versioned paths: `/v1/`, `/v2/` (not query params)
- Fail fast on missing data (404); fall back to last known data when current
  source is unavailable
