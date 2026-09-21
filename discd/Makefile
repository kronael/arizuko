OUT ?= .
build:
	go build -o $(OUT)/discd .

test:
	go test ./... -count=1

clean:
	rm -f discd

.PHONY: build test clean
