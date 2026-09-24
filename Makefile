DAEMONS = authd routd runed onbod dashd proxyd webd timed teled discd emaid bskyd reditd linkd slakd gitd ttsd
# COMPONENTS are sibling pieces with their own Makefile and README. They live
# in this monorepo but are orthogonal: their code does not import
# arizuko-internal packages. egress is what runed starts per spawn (spec 6/24).
COMPONENTS = egress anteval

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

# kernel: the guest kernel arizuko boots per turn (spec 6/41). Its own target and
# NOT a stage of ant/Dockerfile: ant/ took 130 commits in 60 days while this
# changes only when KVER or the fragment does, and `docker builder prune -f` —
# the disk lever the spec names — deletes a build cache, so a kernel living in
# one would be thrown away by the documented cleanup.
#
# The artifact key is the version plus a hash of EVERY input that lands in the
# artifact — the fragment and guest-init's sources. guest-init belongs in the key
# because it ships inside initrd.gz, and the Dockerfile because it decides what the
# artifact contains. Both were learned the same way on 2026-09-24: with the
# fragment alone an init fix left the key unchanged, and with the fragment plus
# guest-init a Dockerfile fix to the initrd's device nodes left it unchanged too.
# Each time the target short-circuited on the existing bzImage and shipped stale.
# EVERY input to the artifact belongs in the key. `make images` depends on this path; it never
# produces it. KERNEL_DIR lands under the instance data dir, which is root-owned,
# so the mkdir carries the same $(DOCKER_SUDO) prefix docker already needs.
KVER ?= 6.1.112
KERNEL_KEY = $(KVER)-$(shell cat kernel/Dockerfile kernel/arizuko.fragment kernel/guest-init/*.go | sha256sum | cut -c1-12)
KERNEL_DIR ?= $(DATA_DIR)/images/kernel-$(KERNEL_KEY)

kernel:
	@if [ -z "$(DATA_DIR)" ]; then echo "kernel: set DATA_DIR=<instance data dir>" >&2; exit 1; fi
	@if [ -f "$(KERNEL_DIR)/bzImage" ]; then \
	  echo "kernel: $(KERNEL_DIR) is current for $(KERNEL_KEY)"; exit 0; \
	fi
	$(DOCKER_BUILD) --target out -t arizuko-kernel:$(KERNEL_KEY) \
	  --build-arg KVER=$(KVER) kernel/
	$(DOCKER_SUDO) mkdir -p $(KERNEL_DIR)
	$(DOCKER) run --rm -v $(KERNEL_DIR):/dst arizuko-kernel:$(KERNEL_KEY) \
	  sh -c 'cp /out/bzImage /out/initrd.gz /out/kernel.config /dst/'
	@echo "kernel: $(KERNEL_DIR)"
.PHONY: kernel

OUT ?= .
DOCKER_TARGETS = $(addprefix docker-build-,arizuko egress $(DAEMONS))

docker-build: $(DOCKER_TARGETS)

docker-build-arizuko:
	CGO_ENABLED=1 go build -o $(OUT)/arizuko ./cmd/arizuko/

# egress is a COMPONENT, not a daemon, and it is in the image anyway: under the
# QEMU substrate runed starts it as a HOST process on the tap's host side, not
# as a container from EGRESS_IMAGE (spec 6/41 §"The runed deploy topology").
docker-build-egress:
	CGO_ENABLED=0 go build -o $(OUT)/egress ./egress/cmd/egress/

$(addprefix docker-build-,$(DAEMONS)): docker-build-%:
	$(MAKE) -C $* OUT=$(OUT) build

.PHONY: docker-build docker-build-arizuko docker-build-egress $(DOCKER_TARGETS)

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
	# guest-init is its OWN module (kernel/guest-init/go.mod), so the root ./...
	# cannot see it — measured: `go list ./... | grep -c guest-init` is 0. Its
	# tests would have been written and then never run by anything. The module is
	# separate on purpose: it is PID 1 in an initrd holding one binary and it
	# takes no dependency it can avoid, so it carries no go.sum.
	cd kernel/guest-init && go test ./... -count=1 -short

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
#
# objstore joined 2026-09-22: it is the storage layer every daemon writes
# through — GuardedBatch and the conformance interleaves — and it was outside
# the list. It costs 13s of the ~25 minutes, measured: objstore 1.0s, dynstore
# 11.0s, dyntest 1.0s, 0 races. The list stays bounded by COST, not derived,
# because -race over the whole tree is not a gate anyone would run.
test-race:
	go test -race -count=1 -timeout 30m ./runed/... ./timed/... ./routd/... ./store/... ./authd/... ./objstore/...
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
	make rootfs

# rootfs exports the agent image's filesystem as a directory tree, which is what
# the QEMU substrate boots read-only per turn (spec 6/41 §"The image and the guest
# kernel"). `docker export` of a created-but-never-started container gives the flat
# filesystem; `docker save` gives layers, which the guest cannot mount, and
# virtio-fs exports a directory, so the tree cannot stay a tarball.
#
# It is keyed on the IMAGE ID, not a tag, for the reason the kernel key hashes its
# inputs: `arizuko-ant:latest` moves under you, and a guest booting last week's
# rootfs against this week's ant is the failure that has no error message.
#
# MEASURED: the tree is ~8.3 GB, against a host that has had 3.5 GB free. There is
# deliberately NO free-space check here — the operator's lever is `docker builder
# prune -f`, and a guard would refuse a build that the lever makes fit.
ROOTFS_KEY = $(shell $(DOCKER) image inspect --format '{{.Id}}' arizuko-ant:latest 2>/dev/null | cut -d: -f2 | cut -c1-12)
ROOTFS_DIR ?= $(DATA_DIR)/images/rootfs-$(ROOTFS_KEY)

rootfs:
	@if [ -z "$(DATA_DIR)" ]; then echo "rootfs: set DATA_DIR=<instance data dir>" >&2; exit 1; fi
	@if [ -z "$(ROOTFS_KEY)" ]; then echo "rootfs: no arizuko-ant:latest image — run \`make agent\` first" >&2; exit 1; fi
	@if [ -d "$(ROOTFS_DIR)/usr" ]; then echo "rootfs: $(ROOTFS_DIR) is current for $(ROOTFS_KEY)"; exit 0; fi
	$(DOCKER_SUDO) mkdir -p $(ROOTFS_DIR)
	# One container, created and exported, never started. `create` takes no --rm,
	# so the id is captured and dropped on exit even when tar fails.
	set -e; \
	  cid=$$($(DOCKER) create arizuko-ant:latest true); \
	  trap "$(DOCKER) rm -f $$cid >/dev/null" EXIT; \
	  $(DOCKER) export $$cid | $(DOCKER_SUDO) tar -x -C $(ROOTFS_DIR)
	@echo "rootfs: $(ROOTFS_DIR)"
	@$(MAKE) --no-print-directory rootfs-stale DATA_DIR=$(DATA_DIR)

# rootfs-stale NAMES the trees beyond the newest two: the one a running turn may
# still have mounted, and the one before it, so a rollback needs no re-export. At
# ~8.3 GB each an unbounded set fills the volume, which is how this host reached
# 99% (BUGS L359).
#
# It REPORTS and does not delete, and that is the design rather than a gap. The
# tree sits in an instance's data dir, a turn may have it mounted through
# virtiofs this second, and a recursive delete of a wrong path removes a release.
# The platform names what is stale; the operator removes it, which is the same
# split every other live-data action in this repo takes.
rootfs-stale:
	@cd $(DATA_DIR)/images 2>/dev/null || exit 0; \
	  stale=$$(ls -1dt rootfs-* 2>/dev/null | tail -n +3); \
	  if [ -z "$$stale" ]; then echo "rootfs-stale: none, two or fewer trees kept"; exit 0; fi; \
	  echo "rootfs-stale: these trees are past the two kept, ~8.3 GB each:"; \
	  for d in $$stale; do echo "  $(DATA_DIR)/images/$$d"; done; \
	  echo "rootfs-stale: a turn may still have one mounted, so removal is yours"

# bench-boot reports the boot split the gate is written against: kernel entry,
# mounts done, ctl up, and QEMU exit (spec 6/41 §"The image and the guest kernel").
#
# It drives the ACCEPTANCE TEST rather than a second boot driver. One renderer,
# many sinks: a separate benchmark harness would render its own argument list and
# then measure a guest the substrate does not start. The test already logs the
# guest's probe map and both host-side times, so -v is the whole report.
#
# Needs /dev/kvm and root. sudo strips PATH, so the toolchain is named explicitly
# — a bare `sudo go` picks an older Go and dies on go.mod's `toolchain` directive.
bench-boot:
	@if [ -z "$(DATA_DIR)" ]; then echo "bench-boot: set DATA_DIR=<instance data dir>" >&2; exit 1; fi
	@if [ ! -f "$(KERNEL_DIR)/bzImage" ]; then \
	  echo "bench-boot: no kernel at $(KERNEL_DIR) — run \`make kernel DATA_DIR=$(DATA_DIR)\`" >&2; exit 1; fi
	@echo "bench-boot: kernel $(KERNEL_DIR), rootfs $(ROOTFS_DIR)"
	@mkdir -p tmp
	sudo -E RUNED_SUBSTRATE=qemu \
	  RUNED_KERNEL_DIR=$(KERNEL_DIR) RUNED_ROOTFS_DIR=$(ROOTFS_DIR) \
	  DATA_DIR=$(DATA_DIR) \
	  $$(command -v go) test ./runed/ -count=1 -v \
	  -run TestGuestBootsAndReportsOverCtl > tmp/bench-boot.log 2>&1 || true
	@grep -E "ctl up|guest probe|qemu exited|FAIL" tmp/bench-boot.log || true
	@# A SKIP exits 0 and would read as a measurement. Print what the host is
	@# missing and fail: a benchmark that measured nothing is not a green one.
	@if ! grep -q "ctl up" tmp/bench-boot.log; then \
	  echo "bench-boot: NOTHING WAS MEASURED. The test did not reach a booted guest:"; \
	  sed -n '/SKIP\|cannot run the QEMU substrate/,$$p' tmp/bench-boot.log | sed -n '1,12p'; \
	  echo "bench-boot: full output in tmp/bench-boot.log"; \
	  exit 1; \
	fi

vite-image:
	$(DOCKER_BUILD) -f ant/Dockerfile.vite -t arizuko-vite:latest .

agent:
	make -C ant image DOCKER="$(DOCKER)"

.PHONY: build lint test clean images agent vite-image rootfs rootfs-stale bench-boot
