DAEMONS = authd routd runed onbod dashd proxyd webd timed teled discd emaid bskyd reditd linkd slakd gitd ttsd
# COMPONENTS are sibling pieces with their own Makefile and README. They live
# in this monorepo but are orthogonal: their code does not import
# arizuko-internal packages. egress is what runed starts per spawn; crackbox is
# the local-development command that composes egress + sandbox (spec 6/24).
COMPONENTS = egress crackbox anteval

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

# lint is the ONE gate, and CI runs exactly this target. Three checks, in the
# order that fails cheapest first:
#
#   gofmt   — formatting drifted silently for want of a gate, and a dirty file
#             makes every later diff noisier than the change it carries.
#   go vet  — one module, so `./...` from here covers every daemon and
#             component; no sub-make repeats it over a subset.
#   golangci-lint — the swallow-class gate (errcheck + nilerr, .golangci.yml).
#             Skipped with a notice when not installed, so the gate degrades to
#             a warning on a machine without it instead of blocking the commit.
lint:
	@out=$$(gofmt -l $(GOFMT_DIRS)); \
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  golangci-lint run; \
	else \
	  echo "note: golangci-lint absent, the errcheck/nilerr gate did not run"; \
	fi

# The tree's own Go, excluding vendored and scratch checkouts.
GOFMT_DIRS = $(shell git ls-files '*.go' | grep -v '^third_party/' | xargs -r dirname | sort -u)

# A package that hits go test's built-in 10m timeout dies with a goroutine dump
# and NO `--- FAIL:` line — only a bare `FAIL <pkg> 600s`, which reads as a flaky
# assertion. BUGS F37 hunted that signature as a slakd race for six weeks; the
# package was outrunning a budget nobody had chosen. This names the budget for
# every invocation that names none. `+=` keeps a caller's own GOFLAGS, and an
# inline `-timeout` on a recipe below still wins, because go's command line beats
# GOFLAGS: integration, test-race, test-e2e and test-turns keep the budgets they
# name (BUGS L26).
export GOFLAGS += -timeout=20m

# Every test that touches a store runs against a store server, so the suite needs
# docker: dyntest starts one per test binary and removes it at the end.
# ARIZUKO_TEST_DYNAMO_ENDPOINT points the same suites at one that is already
# running — the PostgreSQL deployment, or Amazon's own service.
test:
	go test ./... -count=1 -short

# integration: pure integration tests in tests/ (no -short; hits real DBs,
# real HTTP between daemons). Excluded from `make test` due to setup cost.
#
# CI does NOT run this. It runs lint, test and test-e2e
# (.github/workflows/ci.yml). This target is red today — BUGS L349 — and
# wiring a red gate breaks every build, so it is fix first, wire second.
integration:
	# 600s, not 120s: tests/ holds the container-turn cases and their own
	# turnTimeout is 4 minutes, so a 120s package budget killed the binary
	# before any test could report — the F37 signature above, a bare
	# `FAIL <pkg> 124s` with no `--- FAIL:` line (BUGS L349).
	go test ./tests/... -count=1 -timeout 600s
	go test ./objstore/dynstore/... -count=1 -timeout 300s
	go test ./tests/dynamo/... -count=1 -timeout 300s
.PHONY: integration

# test-all: full suite — unit + integration + Playwright. What CI runs on
# a release candidate. Equivalent to: test + integration + play.
test-all: test integration play
.PHONY: test-all

# test-race: race detector on concurrency-critical packages only.
# ~10x slower than make test; run before tagging.
#
# -timeout 30m because two packages exceed go's 10m per-package default under
# -race and the target died on the panic instead of finishing, so the gate the
# release procedure names could never run (BUGS L26). Measured 2026-08-29 on
# this tree: store 856s, routd 612s, 0 races in either.
test-race:
	go test -race -count=1 -timeout 30m ./runed/... ./timed/... ./routd/... ./store/... ./authd/...
.PHONY: test-race

# test-e2e: release-only webd route-token E2E tests (TestE2E*, slow, ≤5 min).
# Run before tagging. `go test` exits 0 even when -run matches nothing, so
# fail explicitly on "no tests to run" — a selector matching zero tests must
# not pass as a green gate.
test-e2e:
	@out=$$(go test ./webd/... -count=1 -run E2E -timeout 300s 2>&1); code=$$?; \
	printf '%s\n' "$$out"; \
	if printf '%s' "$$out" | grep -q 'no tests to run'; then \
		echo "test-e2e: -run E2E matched no tests in ./webd/... — gate would pass vacuously" >&2; \
		exit 1; \
	fi; \
	exit $$code
.PHONY: test-e2e

# test-turns: real container turns against a fake model (tests/e2e_turn_test.go):
# routd + runed + docker + the arizuko-ant image + the agent runtime, with only
# the model endpoint faked through MODEL_GATEWAY_URL. Needs docker (directly or
# via `sudo -n`) and `make agent`; no network, no credential, no spend. Skipped
# under -short, so `make test` never spawns; the vacuous-pass guard is the same
# one test-e2e carries.
test-turns:
	@out=$$(go test ./tests/ -count=1 -run E2ETurn -v -timeout 600s 2>&1); code=$$?; \
	printf '%s\n' "$$out" | grep -vE '"level":"(DEBUG|INFO)"|^[0-9/]+ [0-9:]+ (INFO|WARN) '; \
	if printf '%s' "$$out" | grep -qE 'no tests to run|^--- SKIP'; then \
		echo "test-turns: the container turns did not run — docker or arizuko-ant missing?" >&2; \
		exit 1; \
	fi; \
	exit $$code
.PHONY: test-turns

# play: Playwright browser suite against a throwaway dashd + seeded sqlite.
# Builds seed + dashd binaries on demand. Requires Node + one-time
# `npx playwright install --with-deps chromium` under tests/dashd-playwright/.
play:
	cd tests/dashd-playwright && npx playwright test
.PHONY: play

# test-dash: alias for play (backward compat).
test-dash: play
.PHONY: test-dash

# smoke: post-deploy liveness of a running instance — every container healthy
# (adapters degraded by a down platform link warn, not fail) + egress isolation.
# This is the FREE tier of tests/integration_features_smoke_test.go (no model
# credits); the credit-spending end-to-end tier there is opt-in. Single source:
# the smoke logic lives in one place, run here and by `go test` directly.
# Run after every redeploy: `make smoke SMOKE_INSTANCE=krons`.
SMOKE_INSTANCE ?= krons
smoke:
	SMOKE_INSTANCE=$(SMOKE_INSTANCE) SMOKE_DOCKER='$(DOCKER)' \
	  go test ./tests/ -tags smoke -count=1 -v \
	  -run 'TestSmoke_ContainerHealth|TestSmoke_EgressImage'
.PHONY: smoke

clean:
	rm -f arizuko
	rm -rf tmp/
	$(foreach d,$(DAEMONS),make -C $(d) clean;)
	$(foreach c,$(COMPONENTS),make -C $(c) clean;)

images:
	$(DOCKER) image prune -f
	# Bound the BUILD cache, which `image prune` does not touch. A full rebuild
	# leaves 15-22GB of BuildKit cache that nothing ever collects, so the third
	# rebuild on a busy host dies at 99% disk — twice on 2026-08-18, twice more
	# on 2026-08-20. --keep-storage caps it WITHOUT going cold: an unbounded
	# `builder prune -f` would reclaim the same space and then make every
	# subsequent build recompile every layer. `builder prune` is also the safe
	# verb: `image prune -a` deletes arizuko-ant, which has no container
	# between turns to protect it, and every spawn then 403s.
	$(DOCKER) builder prune -f --keep-storage 8GB
	$(DOCKER_BUILD) -t arizuko .
	$(DOCKER_BUILD) -t arizuko-whatsapp -f whapd/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-twitter -f twitd/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-egress -f egress/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-davd -f davd/Dockerfile .
	$(DOCKER_BUILD) -t arizuko-ttsd -f ttsd/Dockerfile .
	make -C ant image DOCKER="$(DOCKER)"
	make vite-image

vite-image:
	$(DOCKER_BUILD) -f ant/Dockerfile.vite -t arizuko-vite:latest .

agent:
	make -C ant image DOCKER="$(DOCKER)"

.PHONY: build lint test clean images agent vite-image
