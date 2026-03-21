build:
	go build -o timed .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f timed

.PHONY: build test lint clean
