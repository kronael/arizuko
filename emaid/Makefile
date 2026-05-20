OUT ?= .
build:
	go build -o $(OUT)/emaid .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f emaid

.PHONY: build test lint clean
