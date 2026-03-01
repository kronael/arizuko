image := kanipi
agent_image := kanipi-agent

build:
	npx tsc

lint:
	npx tsc --noEmit

test: lint

image:
	docker build -t $(image) .

agent-image:
	./container/build.sh $(agent_image)

clean:
	rm -rf tmp/ dist/

.PHONY: build lint test image agent-image clean
