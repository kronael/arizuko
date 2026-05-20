OUT ?= .
build:
	go build -o $(OUT)/mastd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f mastd

.PHONY: build test lint clean
