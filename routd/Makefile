OUT ?= .
build:
	go build -o $(OUT)/routd ./cmd/routd

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f routd

.PHONY: build test lint clean
