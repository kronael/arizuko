// Command runed runs the runed daemon: the execution plane (spec 5/P-runed) —
// the work queue and the per-spawn container lifecycle. runed never appends a
// message (routd is the sole appender), holds no signing key, and mints no
// token: it verifies its callers against authd's JWKs and spawns containers.
// routd hosts the agent MCP socket in-process.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/substrate"
)

// spawnRetention is how long a finished run stays queryable. It is the bound
// that keeps runed's one spawns partition a window rather than an archive
// (runed/runstore/spawns.go), so it is stated where the sweep that applies it
// lives.
const spawnRetention = 7 * 24 * time.Hour

// newRunedMux builds runed's complete HTTP surface: the Server's routed mux
// plus the two mounts main used to add inline. Extracted so a test can probe
// the routing table runed actually builds — the doc-vs-mux guard has to compare
// the emitted document against the real mux, and mounts made inside main are
// unreachable (BUGS F40).
func newRunedMux(srv *runed.Server) *http.ServeMux {
	mux := srv.Handler().(*http.ServeMux)
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("runed", mux))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	return mux
}

func main() {
	defer obs.Setup("runed", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("runed", os.Getenv("ARIZUKO_INSTANCE"))()

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	// What the Docker substrate delivers on this configuration, decided once
	// at start where the operator is looking (spec 6/25 §3). An error is a
	// half configuration — the image is named and an input the proxy needs is
	// empty — under which every spawn would run unfiltered; refusing to start
	// is the loud version, once at deploy instead of per turn.
	sub, err := runed.Substrate(cfg)
	if err != nil {
		slog.Error("substrate", "err", err)
		os.Exit(1)
	}
	slog.Info("substrate", "backend", sub.Delivered.Backend, "egress", sub.Delivered.Egress,
		"declared_egress", sub.Declared.Egress, "reason", sub.Reason)
	// Where the model credential sits is decided at the same moment. With no
	// gateway, spawnSecrets hands the operator anchor to every container, and
	// inside one it is readable from the agent's shell and from /proc whatever
	// the harness scrubs (BUGS L131). A supported deployment (spec 6/23), so a
	// warning, not a refusal — once, where the operator is looking.
	if container.SpawnHoldsModelCredential(cfg) {
		slog.Warn("no model gateway: the operator model credential enters every agent container " +
			"and is readable from the agent's shell and /proc; set MODEL_GATEWAY_URL " +
			"(the litellm package, spec 6/23) to withhold it")
	}
	// A .env still naming the shared crackbox proxy reads as isolation on to
	// the operator while the substrate delivers none: the admin API it points
	// at does not exist (spec 6/24). Same refusal, same moment.
	if v := os.Getenv("CRACKBOX_ADMIN_API"); v != "" && sub.Delivered.Egress == substrate.EgressNone {
		slog.Error("CRACKBOX_ADMIN_API is set but the shared crackbox proxy was removed (spec 6/24); "+
			"egress is now one proxy per spawn: set EGRESS_IMAGE=arizuko-egress:latest "+
			"(build it with `make images`) and drop CRACKBOX_ADMIN_API, or drop both to run without egress filtering",
			"crackbox_admin_api", v)
		os.Exit(1)
	}
	listenAddr := core.EnvOr("LISTEN_ADDR", ":8080")
	authdURL := os.Getenv("AUTHD_URL")
	runTTL := core.DurOr("RUNED_RUN_TIMEOUT", 20*time.Minute)

	// runed.Open is the door: it dials the instance's store server and makes
	// runed's entities there.
	db, err := runed.Open()
	if err != nil {
		slog.Error("open runed's store", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	// runed's audit sink is its OWN record — each daemon owns its own audit_log;
	// correlation across them is turn_id, not a shared table (spec 5/I-tool-call-logging). Init
	// takes whichever handle runed was opened over, so the sink follows.
	audit.Init(db.Obj(), os.Getenv("ARIZUKO_INSTANCE"))
	if n, err := db.ExpireOrphans(context.Background()); err != nil {
		slog.Error("expire orphan spawns", "err", err)
	} else if n > 0 {
		slog.Warn("runed: expired orphan spawns from prior run", "count", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verifier: offline-verify callers (routd / operator / agent) against
	// authd's JWKs. runed holds no signing key (spec 5/P-runed § Auth).
	var verify runed.Verifier
	// dashKeys arms the /dash/runed/ operator gate from the SAME keyset the /v1
	// verifier uses; nil (no AUTHD_URL) leaves the dashboard open in local dev.
	var dashKeys *auth.KeySet
	if authdURL != "" {
		ks, kerr := auth.FetchKeys(ctx, authdURL)
		if kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
		verify = keysetVerifier{ks: ks}
		dashKeys = ks
	}

	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	runtime := runed.NewDockerRuntime(cfg, folders)

	mgr := runed.NewManager(db, runtime, runed.ManagerConfig{
		RunTTL:        runTTL,
		Instance:      cfg.Name,
		MaxConcurrent: cfg.MaxContainers,
	})
	// POST /v1/holds: let an external process (arizuko archive apply, and any
	// future folder-exclusive job) claim a folder's run slot (spec 5/8).
	mgr.RegisterExecutor(runed.KindHold, runed.NewHoldRuntime())

	// Hourly GC of aged spawns. The RETENTION is this policy's, not the
	// store's: spawns lives in one partition and this sweep is what keeps it a
	// window rather than an archive (runed/runstore/spawns.go).
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := db.DropSpawnsBefore(ctx, time.Now().Add(-spawnRetention))
				if err != nil {
					slog.Error("runed: sweep aged spawns", "err", err)
				} else if n > 0 {
					slog.Info("runed: swept aged spawns", "count", n)
				}
			}
		}
	}()

	// Startup parity with the former gateway.Run (spec 5/P-runed): fail fast if the
	// container runtime is unreachable, then stop any orphaned spawn containers
	// left by a prior crash before we start accepting new runs.
	if err := container.EnsureRunning(); err != nil {
		slog.Error("container runtime check failed", "err", err)
		os.Exit(1)
	}
	container.CleanupOrphans(cfg.Name, cfg.Image)

	// Stage the release's ant/ where agent containers can bind-mount it. Must
	// precede the first spawn — an agent with no /opt/arizuko cannot migrate.
	container.MaterializeAppSrc(cfg)

	srv := runed.NewServer(mgr, db, verify)
	srv.SetDashKeySet(dashKeys)
	srv.SetSubstrate(sub)
	mux := newRunedMux(srv)

	httpd := &http.Server{Addr: listenAddr, Handler: obs.HTTPMiddleware("runed")(mux)}
	// Register the signal handler BEFORE the listener accepts: a SIGTERM in
	// between takes Go's default disposition and kills the process, so the
	// Shutdown below never runs (BUGS L127).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("runed started", "addr", listenAddr, "db", cfg.StoreDir)
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	// Graceful shutdown (spec 5/P-runed § Graceful shutdown): stop accepting new
	// POST /v1/runs and wait up to RUNED_SHUTDOWN_GRACE for the handlers
	// already running to finish.
	//
	// A run only survives runed's own exit. It does NOT survive routd's: the
	// run context is the REQUEST's (Manager.spawn, runed/manager.go), and
	// armCancel (runed/docker.go) kills the container when that context dies
	// — its own comment names "routd dropped the POST /v1/runs request" as
	// one of the two causes. A deploy stops both daemons, so a live turn ends
	// with 137 and routd re-feeds it. Detaching here alone would be worse, not
	// better: the agent's MCP socket lives in ROUTD's process, so a container
	// outliving routd has no socket, holds the folder's run slot, and is
	// re-fed anyway (BUGS L161).
	grace := core.DurOr("RUNED_SHUTDOWN_GRACE", runTTL)
	slog.Info("runed stopping (detaching in-flight runs)", "grace", grace, "in_flight", mgr.ActiveCount(context.Background()))
	sctx, scancel := context.WithTimeout(context.Background(), grace)
	defer scancel()
	_ = httpd.Shutdown(sctx)
	cancel() // stop the GC goroutine only — after handlers have drained.
}

// keysetVerifier adapts auth.FetchKeys → runed.Verifier (offline verify).
type keysetVerifier struct{ ks *auth.KeySet }

func (v keysetVerifier) Verify(r *http.Request) (sub string, scope []string, folder string, err error) {
	s, verr := auth.VerifyHTTP(r, v.ks)
	if verr != nil {
		return "", nil, "", verr
	}
	return s.Sub, s.Scope, s.Extra["arz/folder"], nil
}
