OUT ?= .
build:
	go build -o $(OUT)/proxyd .

test:
	go test ./... -count=1

clean:
	rm -f proxyd

.PHONY: build test clean
