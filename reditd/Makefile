OUT ?= .
build:
	go build -o $(OUT)/reditd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f reditd

.PHONY: build test lint clean
