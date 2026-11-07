export PYTHONPATH := .

.PHONY: check right test smoke
check:
	uv run ruff check .
	uv run ruff format --check .
right:
	uv run basedpyright
test:
	uv run python -X dev -W error -m pytest -m 'not smoke'
smoke:
	uv run python -X dev -W error -m pytest -m smoke
