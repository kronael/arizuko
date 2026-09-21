OUT ?= .
build:
	go build -o $(OUT)/reditd .

test:
	go test ./... -count=1

clean:
	rm -f reditd

.PHONY: build test clean
