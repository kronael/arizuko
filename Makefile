build:
	go build -o arizuko cmd/arizuko/main.go
	make -C channels/telegram build

lint:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f arizuko
	rm -rf tmp/
	make -C channels/telegram clean

image:
	docker build -t arizuko .

agent:
	make -C container image

.PHONY: build lint test clean image agent
