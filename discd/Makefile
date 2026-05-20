OUT ?= .
build:
	go build -o $(OUT)/discd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f discd

.PHONY: build test lint clean
