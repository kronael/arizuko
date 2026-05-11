---
name: testing
description: >
  Testing patterns — *_test.go, testcontainers, table-driven, test
  failure triage, conftest.py. USE for "write tests", "fix this failing
  test", "debug test output", *_test.go, conftest.py, testcontainers
  setup. NOT for e2e infra setup (use ops) or test setup inside a
  language skill.
user-invocable: true
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
