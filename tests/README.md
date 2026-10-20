# tests

This file answers: how do I run the tests, and what does each suite need?
[README.md](../README.md) names the project and [INSTALL.md](../INSTALL.md)
builds it.

## A test that opens a store needs docker

Every store lives on a DynamoDB-protocol server, so a test that opens a store
needs an endpoint. `dyntest` (`objstore/dynstore/dyntest/dyntest.go`) gives
one to each test binary:

```text
go test ./routd/...
        │
        ▼
┌───────────────┐  ARIZUKO_TEST_DYNAMO_ENDPOINT set  ┌───────────────────┐
│ dyntest.Main  ├───────────────────────────────────►│ that endpoint     │
└───────┬───────┘                                    └───────────────────┘
        │ not set
        ▼
┌───────────────────────────────────────────────┐
│ extenddb-dev container, one per test binary,  │
│ removed when the binary exits                 │
└───────────────────────────────────────────────┘
```

- `dyntest` starts the container with `docker`, or with `sudo docker` when the
  test does not run as root. `sudo` must not ask for a password.
- `ARIZUKO_TEST_DYNAMO_ENDPOINT` points the suites at a running endpoint: the
  PostgreSQL deployment or Amazon's own service. `AWS_ACCESS_KEY_ID`,
  `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` then come from the environment.
  `ARIZUKO_TEST_DYNAMO_CA` names the certificate of an endpoint that signed its
  own.
- A test without an endpoint fails. It does not skip, because a skip shows a
  suite that proved nothing as green.

## The make targets run the suites

| Target                             | Runs                                                                                                                                          | CI  |
| ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- | --- |
| `make lint`                        | gofmt, go vet, golangci-lint, and the doc tests `tests/webdocs`, `tests/specrefs`, `tests/bugsindex`                                          | yes |
| `make test`                        | `go test ./... -count=1 -short -timeout 1800s`, the `kernel/guest-init` module, `tests/artifact_cache_test.sh`, `kernel/check-config_test.sh` | yes |
| `make test-e2e`                    | `go test ./webd/... -run E2E`; fails when the selector matches no test                                                                        | yes |
| `make integration`                 | `./tests/...`, `./objstore/dynstore/...`, `./tests/dynamo/...` without `-short`                                                               | no  |
| `make test-race`                   | `-race` on runed, timed, routd, store, authd and objstore                                                                                     | no  |
| `make play`                        | the Playwright suite in `tests/dashd-playwright/`                                                                                             | no  |
| `make smoke SMOKE_INSTANCE=<name>` | the `smoke` build tag against a deployed instance                                                                                             | no  |

One test:

```bash
go test ./routd/... -count=1 -run TestName
```

No make target runs the TypeScript suites. Run them in their directory:
`cd ant && bun test src/`, `cd whapd && bun run test`, `cd twitd && bun run test`.

`kernel/guest-init` is its own Go module. The root `./...` does not include
it, so run `cd kernel/guest-init && go test ./...`.

## Two packages run longer than the Go default timeout

Go stops a test package after 10 minutes by default. On 2026-10-07, routd ran
601 s and store ran 601 s under `go test ./... -count=1 -short`, and both failed
on that default (BUGS `L398`). A package that reaches the timeout prints a goroutine dump and
`FAIL <pkg> 600s`, with no `--- FAIL:` line.

- `make test` passes `-timeout 1800s`. The Makefile also exports
  `GOFLAGS=-timeout=20m` for every `go test` it starts.
- A bare `go test ./...` outside make uses the 10-minute default.
- `make test` uses `-short`, so it skips each test that checks
  `testing.Short()`. `make integration` runs them.
- `tests/buildgate` checks that every gate target in the Makefile states a
  timeout.

## Each directory under tests/ checks one thing

| Path                                       | Checks                                                                                                                                                                    | Needs                                                  |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `tests/*_test.go`                          | cross-daemon behaviour: `feat_*_test.go` (`TestFeature_*`), federation, onboarding, topic lineage, route tokens, media, MCP, the Go and TypeScript `submit_turn` contract | docker                                                 |
| `tests/integration_features_smoke_test.go` | a deployed instance: container health, egress; build tag `smoke`                                                                                                          | a live instance                                        |
| `tests/webdocs/`                           | the web docs: links, shell, sidebar, twin blocks, banned words                                                                                                            | nothing                                                |
| `tests/specrefs/`                          | every link, `depends:` entry and prose spec ID under `specs/`                                                                                                             | nothing                                                |
| `tests/bugsindex/`                         | the "What needs you" list in `BUGS.md` names the open entries                                                                                                             | nothing                                                |
| `tests/buildgate/`                         | the gate targets in the Makefile state their timeout                                                                                                                      | nothing                                                |
| `tests/components/`                        | import rules: `egress/`, `substrate/` and `mcpfw/` import no arizuko package, `container/` does not import `ipc/`, no package imports `database/sql`                      | nothing                                                |
| `tests/resident/`                          | the pointers in the always-loaded `CLAUDE.md` files resolve                                                                                                               | nothing                                                |
| `tests/shutdown/`                          | each daemon arms its signal handler before it listens                                                                                                                     | nothing                                                |
| `tests/socialboundary/`                    | social adapters do not do observer work                                                                                                                                   | nothing                                                |
| `tests/dynamo/`                            | each daemon opens its store from its environment                                                                                                                          | docker                                                 |
| `tests/testutils/`                         | shared helpers, `fakellm.go` and `testutils.go`; no tests                                                                                                                 | —                                                      |
| `tests/dashd-playwright/`                  | every `/dash/` page in a browser                                                                                                                                          | Node and `npx playwright install --with-deps chromium` |
| `tests/lab/product-lab.sh`                 | a manual experiment on products; not a gate                                                                                                                               | —                                                      |

## A new test goes next to the boundary it crosses

- A daemon with an HTTP surface has one boundary test,
  `<daemon>/integration_test.go`: dashd, discd, onbod, proxyd, slakd, teled
  and webd have one. routd's is `routd/authd_integration_test.go`.
- A channel adapter tests its `chanlib.BotHandler` against a stub platform in
  `<adapter>/bothandler_test.go`: bskyd, discd, emaid, linkd, reditd, slakd and
  teled have one.
- A test that crosses two or more daemons goes in `tests/`.
