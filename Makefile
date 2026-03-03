.PHONY: build lint test image clean

build:
	bunx tsc

lint:
	bunx tsc --noEmit

test:
	bunx vitest run

image:
	docker build -t kanipi .
	docker build -t kanipi-agent ./container
	docker build -t whisper ./sidecar/whisper

clean:
	rm -rf tmp/ dist/
