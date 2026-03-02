.PHONY: build lint image clean

build:
	npx tsc

lint:
	npx tsc --noEmit

image:
	docker build -t kanipi .
	docker build -t kanipi-agent ./container

clean:
	rm -rf tmp/ dist/
