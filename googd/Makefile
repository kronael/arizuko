OUT ?= .
build:
	go build -o $(OUT)/googd .

test:
	go test ./... -count=1

clean:
	rm -f googd

.PHONY: build test clean
