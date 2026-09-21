OUT ?= .
build:
	go build -o $(OUT)/routd ./cmd/routd

test:
	go test ./... -count=1

clean:
	rm -f routd

.PHONY: build test clean
