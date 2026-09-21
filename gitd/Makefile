OUT ?= .
build:
	go build -o $(OUT)/gitd .

test:
	go test ./... -count=1

clean:
	rm -f gitd

.PHONY: build test clean
