build:
	go build -o arizuko cmd/arizuko/main.go
	CGO_ENABLED=1 go build -o bin/gated ./gated/
	make -C teled build
	make -C discd build

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f arizuko
	rm -rf bin/ tmp/
	make -C teled clean
	make -C discd clean

images:
	docker build -t arizuko .
	docker build -t arizuko-telegram -f teled/Dockerfile .
	docker build -t arizuko-discord -f discd/Dockerfile .
	make -C container image

agent:
	make -C container image

.PHONY: build lint test clean images agent
