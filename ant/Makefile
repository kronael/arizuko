.PHONY: image test

image:
	docker build -t arizuko-ant .

test:
	bun test src/heartbeat.test.ts
