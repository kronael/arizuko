build:
	go build -o arizuko cmd/arizuko/main.go

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f arizuko
	rm -rf tmp/

image:
	docker build -t arizuko .

agent:
	make -C container image

.PHONY: build lint test clean image agent
