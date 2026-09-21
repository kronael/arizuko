OUT ?= .
build:
	go build -o $(OUT)/bskyd .

test:
	go test ./... -count=1

clean:
	rm -f bskyd

.PHONY: build test clean
