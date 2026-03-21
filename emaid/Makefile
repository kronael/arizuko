build:
	go build -o emaid .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f emaid

.PHONY: build test lint clean
