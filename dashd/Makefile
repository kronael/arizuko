build:
	go build -o dashd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f dashd

.PHONY: build test lint clean
