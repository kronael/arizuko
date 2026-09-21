OUT ?= .
build:
	go build -o $(OUT)/timed .

test:
	go test ./... -count=1

clean:
	rm -f timed

.PHONY: build test clean
