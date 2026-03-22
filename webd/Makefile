build:
	go build -o webd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f webd

.PHONY: build test lint clean
