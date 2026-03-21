build:
	go build -o arizuko cmd/arizuko/main.go
	CGO_ENABLED=1 go build -o bin/gated ./gated/
	go build -o bin/onbod ./onbod/
	go build -o bin/dashd ./dashd/
	make -C teled build
	make -C discd build
	make -C emaid build
	make -C mastod build
	make -C bskyd build
	make -C redd build

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f arizuko
	rm -rf bin/ tmp/
	rm -f bin/dashd
	make -C teled clean
	make -C discd clean
	make -C emaid clean
	make -C mastod clean
	make -C bskyd clean
	make -C redd clean

images:
	docker build -t arizuko .
	docker build -t arizuko-telegram -f teled/Dockerfile .
	docker build -t arizuko-discord -f discd/Dockerfile .
	make -C container image

agent:
	make -C container image

.PHONY: build lint test clean images agent
