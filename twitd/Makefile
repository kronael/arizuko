build:
	bunx tsc

clean:
	rm -rf dist node_modules

image:
	docker build -t arizuko-twitter -f Dockerfile ..

test:
	bun test

lint:
	bunx tsc --noEmit

.PHONY: build clean image test lint
