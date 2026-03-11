build:
	go build -o telegram .

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f telegram

image:
	docker build -t arizuko-telegram .

.PHONY: build test lint clean image
