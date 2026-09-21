OUT ?= .
build:
	go build -o $(OUT)/onbod .

test:
	go test ./... -count=1

clean:
	rm -f onbod

.PHONY: build test clean
