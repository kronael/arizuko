build:
	go build -o agenteval ./cmd/agenteval/

validate:
	go run ./cmd/agenteval validate --cases ./cases

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f agenteval

.PHONY: build validate test lint clean
