.PHONY: image test sync-skills

# The root Makefile calls this with DOCKER="$(DOCKER)" so a host that needs sudo
# for the daemon socket can say so once. Hardcoding `docker` here made
# `make images` build every other image and then fail on this one alone, which
# reads as a finished build until a spawn 403s on the stale arizuko-ant.
DOCKER ?= docker

image:
	$(DOCKER) build -t arizuko-ant .

# The whole suite. This named src/heartbeat.test.ts, a file that does not exist
# — the target exited 1 on every run, so nothing was testing ant from make.
test:
	bun test

# Refresh the vendored skills from kronael/tools. The allow-list and the reason
# each excluded skill is excluded live in the script.
sync-skills:
	./sync-skills.sh
