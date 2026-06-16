DAEMONS = authd routd runed onbod dashd proxyd webd timed teled discd emaid mastd bskyd reditd linkd slakd ttsd
# COMPONENTS are sibling tools shipped alongside arizuko (see specs/11/b).
# They live in this monorepo but are orthogonal: their code does not import
# arizuko-internal packages. Each has its own Makefile, README, and image.
COMPONENTS = crackbox anteval

# DOCKER may be overridden by the caller for hosts where the invoking user is
# in the docker group (then `make images DOCKER=docker`). Default is
# `sudo docker` so `make images` works consistently across dev hosts.
DOCKER ?= sudo docker
# sudo strips env; inject DOCKER_BUILDKIT via `env` after the sudo prefix.
# If DOCKER is overridden to plain `docker`, the env prefix is still harmless.
DOCKER_SUDO = $(filter sudo,$(DOCKER))
DOCKER_BIN  = $(filter-out sudo,$(DOCKER))
DOCKER_BUILD = $(DOCKER_SUDO) env DOCKER_BUILDKIT=1 $(DOCKER_BIN) build

build:
	go build -o arizuko ./cmd/arizuko/
	$(foreach d,$(DAEMONS),make -C $(d) build;)
	$(foreach c,$(COMPONENTS),make -C $(c) build;)

OUT ?= .
DOCKER_TARGETS = $(addprefix docker-build-,arizuko $(DAEMONS))

docker-build: $(DOCKER_TARGETS)

docker-build-arizuko:
	CGO_ENABLED=1 go build -o $(OUT)/arizuko ./cmd/arizuko/

$(addprefix docker-build-,$(DAEMONS)): docker-build-%:
	$(MAKE) -C $* OUT=$(OUT) build

.PHONY: docker-build docker-build-arizuko $(DOCKER_TARGETS)

lint:
	go vet ./...
	$(foreach d,$(DAEMONS),make -C $(d) lint;)
	$(foreach c,$(COMPONENTS),make -C $(c) lint;)

test:
	go test ./... -count=1 -short
	$(foreach d,$(DAEMONS),make -C $(d) test;)
	$(foreach c,$(COMPONENTS),make -C $(c) test;)

# integration: pure integration tests in tests/ (no -short; hits real DBs,
# real HTTP between daemons). Excluded from `make test` due to setup cost.
# CI runs: `make test && make integration`.
integration:
	go test ./tests/... -count=1 -timeout 120s
.PHONY: integration

# test-all: full suite — unit + integration + Playwright. What CI runs on
# a release candidate. Equivalent to: test + integration + play.
test-all: test integration play
.PHONY: test-all

# test-race: race detector on concurrency-critical packages only.
# ~10x slower than make test; run before tagging.
test-race:
	go test -race -count=1 ./runed/... ./timed/... ./routd/... ./store/... ./authd/...
.PHONY: test-race

# test-e2e: release-only webd slink E2E tests (slow, ≤5 min). Run before tagging.
test-e2e:
	go test ./webd/... -count=1 -run E2E -timeout 300s
.PHONY: test-e2e

# play: Playwright browser suite against a throwaway dashd + seeded sqlite.
# Builds seed + dashd binaries on demand. Requires Node + one-time
# `npx playwright install --with-deps chromium` under tests/dashd-playwright/.
play:
	cd tests/dashd-playwright && npx playwright test
.PHONY: play

# test-dash: alias for play (backward compat).
test-dash: play
.PHONY: test-dash

# smoke: post-deploy verification on a running instance. Pings the
# admin and sends a synthetic message through the registered-channel
# path; confirms egress register fires (when on) and the message
# routes. Run after every redeploy: `make smoke INSTANCE=krons`.
SMOKE_INSTANCE ?= krons
smoke:
	@inst=$(SMOKE_INSTANCE); \
	echo "smoking arizuko_$$inst"; \
	for c in $$($(DOCKER) ps --filter "name=arizuko_.*_$$inst" -q); do \
	  name=$$($(DOCKER) inspect -f '{{.Name}}' $$c | tr -d /); \
	  status=$$($(DOCKER) inspect -f '{{.State.Health.Status}}' $$c 2>/dev/null); \
	  if [ -n "$$status" ] && [ "$$status" != "healthy" ]; then \
	    echo "  FAIL: $$name = $$status"; exit 1; \
	  fi; \
	done; \
	echo "  all containers healthy"; \
	if grep -q '^CRACKBOX_ADMIN_API=' /srv/data/arizuko_$$inst/.env 2>/dev/null; then \
	  $(DOCKER) exec arizuko_runed_$$inst wget -qO- --timeout=3 http://crackbox:3129/health \
	    | grep -q '"status":"ok"' && echo "  crackbox /health: ok" \
	    || (echo "  FAIL: crackbox /health"; exit 1); \
	fi
.PHONY: smoke

clean:
	rm -f arizuko
	rm -rf tmp/
	$(foreach d,$(DAEMONS),make -C $(d) clean;)
	$(foreach c,$(COMPONENTS),make -C $(c) clean;)

images:
	$(DOCKER) image prune -f
	$(DOCKER_BUILD) -t arizuko .
	$(DOCKER_BUILD) -t arizuko-whatsapp -f whapd/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-twitter -f twitd/Dockerfile .
	$(DOCKER_BUILD) -t crackbox -f crackbox/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-davd -f davd/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-ttsd -f ttsd/Dockerfile .
	make -C ant image DOCKER="$(DOCKER)"
	make vite-image

vite-image:
	$(DOCKER_BUILD) -f ant/Dockerfile.vite -t arizuko-vite:latest .

agent:
	make -C ant image DOCKER="$(DOCKER)"

.PHONY: build lint test clean images agent vite-image
