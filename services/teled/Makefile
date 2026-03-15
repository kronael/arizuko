build:
	go build -o teled .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f teled

image:
	docker build -t arizuko-telegram .

.PHONY: build test lint clean image
