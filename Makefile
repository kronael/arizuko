DAEMONS = gated onbod dashd proxyd webd timed teled discd emaid mastd bskyd reditd linkd
# COMPONENTS are sibling tools shipped alongside arizuko (see specs/8/b).
# They live in this monorepo but are orthogonal: their code does not import
# arizuko-internal packages. Each has its own Makefile, README, and image.
COMPONENTS = crackbox

# DOCKER may be overridden by the caller for hosts where the invoking user is
# in the docker group (then `make images DOCKER=docker`). Default is
# `sudo docker` so `make images` works consistently across dev hosts.
DOCKER ?= sudo docker

build:
	go build -o arizuko ./cmd/arizuko/
	$(foreach d,$(DAEMONS),make -C $(d) build;)
	$(foreach c,$(COMPONENTS),make -C $(c) build;)

lint:
	go vet ./...
	$(foreach d,$(DAEMONS),make -C $(d) lint;)
	$(foreach c,$(COMPONENTS),make -C $(c) lint;)

test:
	go test ./... -count=1
	$(foreach d,$(DAEMONS),make -C $(d) test;)
	$(foreach c,$(COMPONENTS),make -C $(c) test;)

clean:
	rm -f arizuko
	rm -rf tmp/
	$(foreach d,$(DAEMONS),make -C $(d) clean;)
	$(foreach c,$(COMPONENTS),make -C $(c) clean;)

images:
	$(DOCKER) image prune -af
	$(DOCKER) build -t arizuko .
	$(DOCKER) build -t arizuko-whatsapp -f whapd/Dockerfile .
	$(DOCKER) build -t arizuko-twitter -f twitd/Dockerfile .
	$(DOCKER) build -t crackbox -f crackbox/Dockerfile .
	make -C ant image DOCKER="$(DOCKER)"
	make vite-image

vite-image:
	$(DOCKER) build -f ant/Dockerfile.vite -t arizuko-vite:latest .

agent:
	make -C ant image DOCKER="$(DOCKER)"

.PHONY: build lint test clean images agent vite-image
