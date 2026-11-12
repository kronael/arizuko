.PHONY: image test sync-skills pi-smoke

# The root Makefile calls this with DOCKER="$(DOCKER)" so a host that needs sudo
# for the daemon socket can say so once. Hardcoding `docker` here made
# `make images` build every other image and then fail on this one alone.
DOCKER ?= docker

image:
	$(DOCKER) build -t arizuko-ant .

# The whole suite. This named src/heartbeat.test.ts, a file that does not exist
# — the target exited 1 on every run, so nothing was testing ant from make.
test:
	bun test

# The installed pi against a scripted provider and a recording gated.sock, both
# on the container's loopback. Needs a built image (`make image`).
pi-smoke:
	$(DOCKER) run --rm --network none --entrypoint bun \
	  --mount type=bind,src=$(CURDIR)/smoke/pi-models.json,dst=/mnt/pi/models.json,readonly \
	  arizuko-ant /app/smoke/pi-bridge.ts

# Refresh the vendored skills from kronael/tools. The allow-list and the reason
# each excluded skill is excluded live in the script.
sync-skills:
	./sync-skills.sh
