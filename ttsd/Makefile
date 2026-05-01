build:
	go build -o ttsd .

test:
	go test ./... -count=1

lint:
	go vet ./...

image:
	docker build -t arizuko-ttsd:latest -f Dockerfile ..

clean:
	rm -f ttsd

.PHONY: build test lint image clean
