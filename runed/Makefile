OUT ?= .
build:
	go build -o $(OUT)/runed ./cmd/runed

test:
	go test ./... -count=1

clean:
	rm -f runed

.PHONY: build test clean
