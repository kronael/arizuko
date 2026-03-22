build:
	go build -o proxyd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f proxyd

.PHONY: build test lint clean
