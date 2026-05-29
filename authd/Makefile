OUT ?= .
build:
	go build -o $(OUT)/authd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f authd

.PHONY: build test lint clean
