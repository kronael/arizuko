DAEMONS = gated onbod dashd proxyd timed teled discd emaid mastd bskyd reditd

build:
	go build -o arizuko cmd/arizuko/main.go
	$(foreach d,$(DAEMONS),make -C $(d) build;)

lint:
	go vet ./...
	$(foreach d,$(DAEMONS),make -C $(d) lint;)

test:
	go test ./... -count=1
	$(foreach d,$(DAEMONS),make -C $(d) test;)

clean:
	rm -f arizuko
	rm -rf tmp/
	$(foreach d,$(DAEMONS),make -C $(d) clean;)

images:
	docker build -t arizuko .
	docker build -t arizuko-telegram -f teled/Dockerfile .
	docker build -t arizuko-discord -f discd/Dockerfile .
	make -C container image
	make vite-image

vite-image:
	sudo docker build -f container/Dockerfile.vite -t arizuko-vite:latest .

agent:
	make -C container image

.PHONY: build lint test clean images agent vite-image
