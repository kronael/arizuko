build:
	go build -o reditd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f reditd

.PHONY: build test lint clean
