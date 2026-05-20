OUT ?= .
build:
	go build -o $(OUT)/slakd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f slakd

.PHONY: build test lint clean
