---
name: go
description: >
  Write or modify Go code. Use when working with .go files,
  go.mod, tests, goroutines, or Go build tooling.
---

# Go

## Concurrency

Single goroutine owns all state: direct access, no locks, deterministic order.
Fail fast on conflicts instead of retrying with mutexes.

## Parsing and types

- Parse at the boundary, pass typed values inward
- Platform-specific wire types (API response, DB row) stay in the package that
  owns that boundary
- Shared domain types in `core/`; DTOs adjacent to their handler
- One canonical parse path per format — import, don't reimplement

## Naming

- Full words: `rateLimiter` not `rl`, `group` not `g`, `upstream` not `up`
- Short names OK only for tiny functions (<=5 lines), loop indices, and
  standard abbreviations (`buf`, `err`, `ctx`, `ok`)

## Testing

- `*_test.go` next to code
- `if testing.Short() { t.Skip() }` for slow tests
