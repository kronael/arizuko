---
status: draft
---

# specs/7/26 — consolidate work into the daemon that owns it

Consolidation removes a process when an existing daemon can reuse its work.
proxyd serves the web files; routd runs the task ticker.
The package and daemon contract is [6/59-local-daemons](../6/59-local-daemons.md).
There is no requirement for one binary or one core process.
Storage remains external under [6/43-shared-store-server](../6/43-shared-store-server.md).

## Memory savings decide which processes to fold

The owner supplied these measurements from `sudo docker stats` on 2026-11-07:
krons used about 342 MiB; nine small Go daemons together used 112.6 MiB,
about 12.5 MiB each. Those were runed, proxyd, authd, timed, two teled,
onbod, webd and dashd. Merging all nine has an estimated saving of 60–80 MiB,
not a measured result.

The larger costs were routd at 103 MiB on krons and 389 MiB on sloth,
vited at 92 MiB and 44 MiB respectively, and shared extenddb.service at
4,076 MiB. The shared store figure is host-wide, not a per-instance saving.
The deployed revision was not supplied with these measurements.
Code pointers below were checked at `e3ff575308c94a162f3c7e961863b4880bf411fa`.

Vited supplies static serving and reload machinery (`ant/vite.config.js:1`).
The owner drops reload entirely. timed owns no database; its loop polls routd
at `timed/split.go:244`, while routd already claims tasks at `routd/server.go:697`.
These folds remove duplicated runtime overhead and keep the existing owners.
Optimizing extenddb or routd needs a separate measured cause, not a broad merge.

## Two folds preserve the useful process boundaries

```text
browser ──► proxyd (changed) ── read-only mount (new) ──► web files

startup / tick (changed) ──► routd (changed) ──► runed ──► isolated guest
                            due-task owner

vited process (removed)     timed process (removed)
```

proxyd stays: replacing its configuration and auth logic with Caddy or Traefik
has no demonstrated code saving. authd retains its independent issuer contract.
The standalone evidence is [6/16-daemon-standalone-matrix](../6/16-daemon-standalone-matrix.md).
Its vited, timed and proxy-replacement recommendations are not decisions for this
composition. Process separation serves independent use, platform connection
lifecycles or execution isolation.
A Go package boundary alone does not require a process.

## Module map

```yaml
- path: proxyd/main.go (changed)
  job: replace the Vite upstream with a local file handler behind existing gates
  names: []
  never: replace proxyd or relax route and folder authorization
- path: proxyd/static.go (new)
  job: apply the three serving rules and deny rules around http.FileServer
  names: []
  never: watch files or inject browser code
- path: proxyd/static_test.go (new)
  job: prove serving, redirects, containment and deny behavior
  names: []
  never: require a Vite process
- path: proxyd/main_test.go (changed)
  job: run public and private route checks against local files
  names: []
  never: replace authorization checks with a stub
- path: compose/compose.go (changed)
  job: mount web read-only in proxyd and remove vited and timed services
  names: [Generate]
  never: add another supervisor
- path: compose/compose_test.go (changed)
  job: assert removed services and the proxyd file mount
  names: []
  never: keep a Vite healthcheck
- path: cmd/arizuko/packages.go (changed)
  job: remove vited and timed dependency claims until 6/59 removes the list
  names: []
  never: retain nonexistent services
- path: authd/http.go (changed)
  job: remove the timed service principal
  names: []
  never: widen routd's external scopes for its internal ticker
- path: store/owner_reach.go (changed)
  job: remove the empty timed entry
  names: [StoreReach]
  never: move table ownership
- path: routd/schedule.go (new)
  job: reuse timed firing behavior through routd's existing task and message operations
  names: []
  never: poll itself over HTTP or add a second scheduler
- path: routd/schedule_test.go (new)
  job: move timed firing, retry and idempotency assertions to the owning process
  names: []
  never: weaken scheduled-time identity
- path: routd/cmd/routd/main.go (changed)
  job: start and stop one task ticker with the router context
  names: []
  never: leave the ticker alive after shutdown
- path: routd/server.go (changed)
  job: share task operations with the ticker and retain gated external APIs
  names: [Server]
  never: change the external task contract to fit an internal call
- path: routd/dash.go (changed)
  job: reuse the scheduler status view under the router dashboard
  names: []
  never: create a second task management surface
- path: dashd/services.go (changed)
  job: withdraw the timed tile and link scheduling to routd
  names: []
  never: advertise a removed process
- path: timed/main.go (removed)
  job: remove the scheduler process entrypoint
  names: []
  never: retain a runnable shell around routd
- path: timed/split.go (removed)
  job: move firing semantics into routd and delete the HTTP client
  names: []
  never: leave two firing loops
- path: timed/dash.go (removed)
  job: move useful scheduler status rendering into routd
  names: []
  never: preserve a separate timed server
- path: timed/Makefile (removed)
  job: remove the timed build target
  names: []
  never: build a removed daemon
- path: timed/*_test.go (removed after assertions move)
  job: transfer firing and dashboard coverage to routd and delete split-only checks
  names: []
  never: discard retry and idempotency coverage
- path: Makefile (changed)
  job: remove timed build and test enumeration
  names: [DAEMONS]
  never: keep dead targets
- path: Dockerfile (changed)
  job: remove the timed executable from the release image
  names: []
  never: change the agent image contract
- path: ant/Dockerfile.vite (removed)
  job: remove the Vite image
  names: []
  never: replace it with another static-server container
- path: ant/vite.config.js (removed)
  job: remove Vite serving and reload configuration
  names: []
  never: transfer the watcher
- path: template/web/package.json (removed)
  job: remove the Vite-only web dependency
  names: []
  never: add a build step for static pages
- path: template/web/vite.config.ts (removed)
  job: remove the second Vite configuration
  names: []
  never: preserve a development reload mode
- path: deploy/helm/arizuko/templates/vited.yaml (removed)
  job: remove the Vite workload
  names: []
  never: preserve a second deployment topology for it
- path: deploy/helm/arizuko/templates/timed.yaml (removed)
  job: remove the scheduler workload
  names: []
  never: duplicate routd's ticker
- path: deploy/helm/arizuko/templates/proxyd.yaml (changed)
  job: mount the web volume read-only
  names: []
  never: route static requests to vited
- path: deploy/helm/arizuko/values.yaml (changed)
  job: remove vited and timed settings
  names: []
  never: keep dead configuration
- path: vited/README.md (removed)
  job: retire Vite component instructions
  names: []
  never: claim reload support
- path: timed/README.md (removed)
  job: place scheduling documentation with routd
  names: []
  never: document a separate scheduler process
- path: template/web/pub/arizuko/components/vited.html (removed)
  job: withdraw the Vite component page
  names: []
  never: leave navigation pointing here
- path: template/web/pub/arizuko/components/timed.html (removed)
  job: move scheduling explanation into the router component page
  names: []
  never: advertise a separate daemon
```

Deployment and documentation references change with implementation: image builds,
environment examples, Helm ingress, component navigation and proxyd/routd docs.

## proxyd serves files without reload

```text
request ── existing route/auth/folder gates ──► local file handler (new)
                                                      │
                          directory/rewrite/301 rules │ (changed)
                                                      ▼
                                             http.FileServer (new)
watcher / reload / injected script (removed)
```

Current public file dispatch, `proxyd/main.go:539`:

```go
s.viteProxy.ServeHTTP(w, r)
```

Proposed at each existing static dispatch site:

```go
s.files.ServeHTTP(w, r)
```

The handler wraps `http.FileServer` rooted at `core.Config.WebDir`
(`core/config.go:236`). Compose mounts only the web tree read-only.
Keep the existing public-route checks, private folder checks, vhost behavior
and configured public redirect. Only the static origin changes.

Carry three rules from `ant/vite.config.js:23`:

- A directory request serves its `index.html` when present.
- A static-origin path outside `/pub/` and `/priv/` resolves under `/pub/`.
  Root files outside those trees never become public through a fallback.
- An existing directory without a trailing slash returns 301 with the query
  preserved. Files and missing paths do not receive invented directory redirects.

Deny `.env`, `.env.*`, `*.db` and `.git/**` at every depth, including encoded
paths and symlink targets. Resolved paths must stay inside the authorized tree;
private links cannot escape into another folder. These are containment checks,
not additional filename deny patterns.
There is no watcher, reload endpoint, SSE event, injected script, Vite client
or stylesheet swap. Pages update when the user reloads the browser.
The owner-supplied 44–92 MiB Vite footprint is gross process removal; net savings
must subtract any increase in proxyd's memory.

## routd owns the ticker and task effects

```text
startup / 60-second tick (changed) ──► DB.DueTasks ──► enqueue / log / reschedule
shutdown context (changed) ─────────► stop and join
```

Current tick at `timed/split.go:96` calls `fireSplit`, which claims through
HTTP at `timed/split.go:244`. The owning handler already calls the store,
`routd/server.go:697`:

```go
tasks, err := s.db.DueTasks(time.Now())
```

Proposed ticker body inside routd, schematic:

```go
tasks, err := db.DueTasks(now)
// Reuse the fire loop with direct enqueue, run-log and reschedule operations.
```

Run once at startup, then every 60 seconds under routd's cancellation context.
Keep one loop and wait for its exit before closing the store.
Reuse the existing claim, message ingress, dedup, run-log and reschedule logic.
The ticker does not construct HTTP requests or bypass message validation.
Existing external task endpoints retain their gates.

Preserve `sched-<taskID>@<scheduled-time>` identity, one-shot retry time,
cron advancement, isolated-context sender behavior and explicit failure reports
from `timed/split.go:176`. Move those tests with the behavior.
Show last tick, errors and firing counts in routd's existing dashboard.
Remove timed's service key, container, binary, route and tile.
This does not prescribe routd's internal organization; that is a separate review.

## Open questions

1. **Fold onbod into routd?** onbod opens its own store and routd's store at
   `onbod/main.go:79` and `onbod/main.go:90`; admission tables and audits belong
   to onbod (`onbod/db.go:21`). routd already handles unrouted chats and calls
   onbod (`routd/cmd/routd/main.go:238`).
   Recommendation: evaluate an in-process admission module in routd, retaining
   the onbod table owner and gates. Pages can move to webd only if that reuses
   its existing surface without a new cross-daemon API. Approve the boundary
   after measuring net memory and the exact reused code. A table migration or
   a new webd protocol would be a separate design, not a free process fold.
2. **Fold dashd or other small daemons?** dashd imports platform code
   (`dashd/services.go:16`),
   and onbod/dashd each belong to the measured small-daemon group. Import coupling
   does not establish saved work or net memory (`dashd/main.go:296`).
   Recommendation: defer further folds until a comparison identifies both.
   Keep useful standalone services and platform-adapter processes. Keep runed's
   execution isolation under [6/41-qemu-substrate](../6/41-qemu-substrate.md).
   The 12.5 MiB average is not an individual or net saving for any candidate.

## Contract

The owner measurements above are the baseline, not an achieved saving.
Before implementation, record idle and matched-workload memory with identical
packages, data, build and sampling interval. Repeat after each fold.
Measure the host-wide store separately. Accept a fold only when instance memory
falls; do not sum shared memory as if each container owns it.

Acceptance checks:

1. Remove vited and timed from generated deployment and images. A fresh instance
   still serves files and fires scheduled tasks. Neither process restarts.
2. Serve nested indexes, bare directory 301s with queries, public fallback,
   missing files, MIME types and range requests. Deny the four filename patterns,
   traversal and symlink escapes. Preserve private-folder and route authorization.
3. Change HTML, CSS and JS on disk. A fresh request sees the files, but an open
   page receives no injected client, watcher event or automatic reload.
4. Exercise one-shot and cron tasks, failed enqueue and reschedule, a repeated
   claim, router restart and shutdown. Preserve fire identity and task state.
   No loop continues after store closure; no internal scheduler HTTP call remains.
5. Exercise an outside package under 6/59, the isolated guest and the minimum
   behavior in [8/60-minimum-arizuko](../8/60-minimum-arizuko.md).
   Consolidation changes neither table authority nor package discovery.
6. Remove Vite and timed documentation, image hooks and stale links in the same
   implementation. Run build, lint, short tests, spec-reference and webdoc checks.
   Report net memory per fold; leave onbod/dashd unmerged until their questions
   have measured answers.
