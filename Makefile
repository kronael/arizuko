.PHONY: build lint test smoke image clean

build:
	bunx tsc

lint:
	bunx tsc --noEmit
	cd container/agent-runner && npm install --silent && npx tsc --noEmit

test:
	bunx vitest run src tests/e2e

smoke:
	bunx vitest run

image:
	docker build -t kanipi .

clean:
	rm -rf tmp/ dist/
