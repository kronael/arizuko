build:
	go build -o egred .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f egred

.PHONY: build test lint clean
