.PHONY: build lint test image whisper clean

build:
	bunx tsc

lint:
	bunx tsc --noEmit

test:
	bunx vitest run

image:
	docker build -t kanipi .
	docker build -t kanipi-agent ./container

whisper:
	cd sidecar/whisper && uvx --with-requirements requirements.txt uvicorn main:app --host 0.0.0.0 --port 8178

clean:
	rm -rf tmp/ dist/
