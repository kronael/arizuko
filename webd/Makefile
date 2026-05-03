build:
	go build -o webd .

test:
	go test ./... -count=1 -short

# test-e2e: real slink-driven end-to-end tests. Excluded from `make test`
# via testing.Short(). Run on release; see root Makefile test-e2e.
test-e2e:
	go test ./... -count=1 -run E2E -timeout 300s
.PHONY: test-e2e

lint:
	go vet ./...

clean:
	rm -f webd

.PHONY: build test lint clean
