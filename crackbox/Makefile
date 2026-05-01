PREFIX  ?= /usr/local
BINDIR   = $(PREFIX)/bin
LIBEXEC  = $(PREFIX)/libexec/crackbox

build:
	go build -o crackbox ./cmd/crackbox/

install: build
	install -d $(BINDIR) $(LIBEXEC)
	install -m 755 crackbox  $(BINDIR)/crackbox
	install -m 755 libexec/* $(LIBEXEC)/

test:
	go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -f crackbox

.PHONY: build install test lint clean
