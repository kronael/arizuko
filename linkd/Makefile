OUT ?= .
build:
	go build -o $(OUT)/linkd .

test:
	go test ./... -count=1

clean:
	rm -f linkd

.PHONY: build test clean
