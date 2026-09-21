OUT ?= .
build:
	go build -o $(OUT)/teled .

test:
	go test ./... -count=1

clean:
	rm -f teled

.PHONY: build test clean
