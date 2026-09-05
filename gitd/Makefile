OUT ?= .
build:
	go build -o $(OUT)/gitd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f gitd

.PHONY: build test lint clean
