OUT ?= .
build:
	CGO_ENABLED=1 go build -o $(OUT)/gated .

test:
	CGO_ENABLED=1 go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f gated

.PHONY: build test lint clean
