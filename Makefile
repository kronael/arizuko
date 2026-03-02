.PHONY: build lint image clean

build:
	npx tsc

lint:
	npx tsc --noEmit

image:
	docker build -t kanipi .
	./container/build.sh kanipi-agent

clean:
	rm -rf tmp/ dist/
