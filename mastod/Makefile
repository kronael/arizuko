build:
	go build -o mastod .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f mastod

.PHONY: build test lint clean
