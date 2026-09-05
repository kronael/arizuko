BIN := mcpfw

.PHONY: build test lint clean

build:
	go build -o $(BIN) ./cmd/mcpfw

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f $(BIN)
