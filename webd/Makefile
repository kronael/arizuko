OUT ?= .
build:
	go build -o $(OUT)/webd .

test:
	go test ./... -count=1 -short

# test-e2e: real slink-driven end-to-end tests. Excluded from `make test`
# via testing.Short(). Run on release; see root Makefile test-e2e.
# `go test` exits 0 even when -run matches nothing, so fail explicitly on
# "no tests to run" — a selector matching zero tests must not pass as a
# green gate (mirrors the root Makefile's test-e2e guard).
test-e2e:
	@out=$$(go test ./... -count=1 -run E2E -timeout 300s 2>&1); code=$$?; \
	printf '%s\n' "$$out"; \
	if printf '%s' "$$out" | grep -q 'no tests to run'; then \
		echo "test-e2e: -run E2E matched no tests in ./webd/... — gate would pass vacuously" >&2; \
		exit 1; \
	fi; \
	exit $$code
.PHONY: test-e2e

lint:
	go vet ./...

clean:
	rm -f webd

.PHONY: build test lint clean
