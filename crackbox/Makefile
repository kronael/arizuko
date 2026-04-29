build:
	go build -o crackbox ./cmd/crackbox/

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f crackbox

.PHONY: build test lint clean
