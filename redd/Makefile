build:
	go build -o redd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f redd

.PHONY: build test lint clean
