build:
	bunx tsc

clean:
	rm -rf dist node_modules

image:
	docker build -t arizuko-twitter -f Dockerfile ..

test:
	bun test

.PHONY: build clean image test
