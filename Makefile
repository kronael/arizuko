.PHONY: build lint test image clean

build:
	bunx tsc

lint:
	bunx tsc --noEmit

test:
	bunx vitest run

image:
	docker build -t kanipi .

clean:
	rm -rf tmp/ dist/
