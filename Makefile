build:
	go build -o arizuko cmd/arizuko/main.go
	CGO_ENABLED=1 go build -o gated ./services/gated/
	make -C services/teled build

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f arizuko gated
	rm -rf tmp/
	make -C services/teled clean

images:
	docker build -t arizuko .
	docker build -t arizuko-telegram -f services/teled/Dockerfile .
	make -C container image

agent:
	make -C container image

.PHONY: build lint test clean images agent
