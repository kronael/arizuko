---
name: testing
description: >
  Use when writing tests, debugging test failures, or setting up
  test infrastructure. Covers unit, e2e, smoke, testcontainers.
---

# Testing

## Diagnosing failures

Capture once, never re-run to analyze output:

```bash
make test 2>&1 | tee ./tmp/test.log && tail -8 ./tmp/test.log && grep -E 'FAIL|fail' ./tmp/test.log
```

For complex failures, hand the log path to a subagent.

## Naming

- unit: fast, no external deps (<5s)
- e2e: self-contained (testcontainers)
- smoke: against a running API (pytest + playwright)

## Pitfalls

- Remove real API/DB tests from the unit suite
- Shared fixtures: `conftest.py` (Python), `tests/common/mod.rs` (Rust)
- Return `Result<()>` for clean error propagation
