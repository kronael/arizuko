OUT ?= .
build:
	go build -o $(OUT)/dashd .

test:
	go test ./... -count=1

clean:
	rm -f dashd

.PHONY: build test clean
