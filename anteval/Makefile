build:
	go build -o anteval ./cmd/anteval/

validate:
	go run ./cmd/anteval validate --cases ./cases

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f anteval

.PHONY: build validate test lint clean
