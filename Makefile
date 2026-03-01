image := kanipi
agent_image := kanipi-agent

build:
	npx tsc

lint:
	npx tsc --noEmit

test: lint

image:
	docker build -t $(image) .

image-agent:
	./container/build.sh $(agent_image)

clean:
	rm -rf tmp/ dist/

.PHONY: build lint test image image-agent clean
