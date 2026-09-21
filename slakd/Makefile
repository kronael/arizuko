OUT ?= .
build:
	go build -o $(OUT)/slakd .

test:
	go test ./... -count=1

clean:
	rm -f slakd

.PHONY: build test clean
