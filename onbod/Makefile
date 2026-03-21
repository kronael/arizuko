build:
	go build -o onbod .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f onbod

.PHONY: build test lint clean
