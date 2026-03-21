build:
	go build -o mastd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f mastd

.PHONY: build test lint clean
