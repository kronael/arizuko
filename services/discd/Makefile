build:
	go build -o discd .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f discd

image:
	docker build -t arizuko-discord .

.PHONY: build test lint clean image
