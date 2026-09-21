OUT ?= .
build:
	go build -o $(OUT)/emaid .

test:
	go test ./... -count=1

clean:
	rm -f emaid

.PHONY: build test clean
