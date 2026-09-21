build:
	go build -o anteval ./cmd/anteval/

validate:
	go run ./cmd/anteval validate --cases ./cases

test:
	go test ./... -count=1

clean:
	rm -f anteval

.PHONY: build validate test clean
