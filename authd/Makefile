OUT ?= .
build:
	go build -o $(OUT)/authd .

test:
	go test ./... -count=1

clean:
	rm -f authd

.PHONY: build test clean
