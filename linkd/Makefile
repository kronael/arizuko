OUT ?= .
build:
	go build -o $(OUT)/linkd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f linkd

.PHONY: build test lint clean
