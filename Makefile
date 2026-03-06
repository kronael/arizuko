.PHONY: build lint test smoke image clean

build:
	bunx tsc

lint:
	bunx tsc --noEmit

test:
	vitest run src

smoke:
	vitest run

image:
	docker build -t kanipi .

clean:
	rm -rf tmp/ dist/
