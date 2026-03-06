build:
	go build -o kanipi cmd/kanipi/main.go

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f kanipi
	rm -rf tmp/

image:
	docker build -t kanipi .

agent:
	make -C container image

.PHONY: build lint test clean image agent
