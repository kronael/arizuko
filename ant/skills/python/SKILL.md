---
name: python
description: Python code patterns — async/await, FastAPI, pytest, packaging.
when_to_use: Use when working with .py files, FastAPI, pytest, or Python packaging.
---

# Python

## Types

- `dict[str, float]`, `list[str]`, `Type | None` (no `Dict`/`List`/`Optional`)

## Style

- `log = logging.getLogger(__name__)` at module/class level (always named `log`)
- `datetime.fromtimestamp(ts, tz=timezone.utc)` not `utcfromtimestamp`
- `.get()` for dict existence checks
- One assignment per line (no `a, b, c = x, y, z`)
- NEVER modify `sys.path`
- NEVER `global` except trivial scripts or signal handlers

## Async

- NEVER manually close async context managers (corrupts asyncpg)
- Return batches, don't yield individual items

## Named data

Prefer dataclass/NamedTuple over `tuple[...]` for return types. Skip for
trivial or test code.

## Build / test

- `uv run --python 3.14` for scripts (system is 3.11), `uvx` for one-off tools
- `uv add` for packages, NEVER bare `pip`
- Test files: `test_*.py` next to code
- `python -m pytest` (not `pytest`) for correct package discovery

## Subprocesses

- `start_new_session=True` on `create_subprocess_exec`
- Kill process groups: `os.killpg(os.getpgid(proc.pid), signal.SIGKILL)`
