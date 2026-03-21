build:
	go build -o bskyd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f bskyd

.PHONY: build test lint clean
