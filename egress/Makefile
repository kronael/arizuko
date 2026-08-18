build:
	go build -o egress ./cmd/egress/

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f egress

.PHONY: build test lint clean
