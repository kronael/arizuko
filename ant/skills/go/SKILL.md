---
name: go
description: >
  Write or modify Go code. Use when working with .go files,
  go.mod, tests, goroutines, or Go build tooling.
---

# Go

## Concurrency

- Single goroutine owns all state: direct access, no locks, deterministic order
- Fails fast on conflicts instead of retrying with mutexes

## Testing

- Test files: `*_test.go` next to code
- Skip slow tests: `if testing.Short() { t.Skip() }`
