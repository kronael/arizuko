OUT ?= .
build:
	go build -o $(OUT)/ttsd .

test:
	go test ./... -count=1

image:
	docker build -t arizuko-ttsd:latest -f Dockerfile ..

clean:
	rm -f ttsd

.PHONY: build test image clean
