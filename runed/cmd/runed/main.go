// Command runed runs the runed daemon: the execution plane (spec 5/P) —
// the work queue, per-spawn container lifecycle, per-tenant MCP socket, and
// per-spawn capability-token brokering. runed never appends a message
// (routd is the sole appender) and never signs a token (authd is the sole
// signer); it brokers one downscoped token per spawn and forwards the
// agent's conversation tools back to routd's /v1/turns/{turn_id}/*.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/types"
)

func main() {
	defer obs.Setup("runed", os.Getenv("ARIZUKO_INSTANCE"))()

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	authdURL := os.Getenv("AUTHD_URL")
	runTTL := durOr("RUNED_RUN_TIMEOUT", 20*time.Minute)

	db, err := runed.Open(cfg.StoreDir)
	if err != nil {
		slog.Error("open runed.db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verifier: offline-verify callers (routd / operator / agent) against
	// authd's JWKs. runed holds no signing key (spec 5/P § Auth).
	var verify runed.Verifier
	if authdURL != "" {
		ks, kerr := auth.FetchKeys(ctx, authdURL)
		if kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
		verify = keysetVerifier{ks: ks}
	}

	// Broker: downscope a capability token per spawn from authd. runed mints
	// nothing; the downscope PARENT is runed's own service:runed token. The
	// HMAC→ES256 cutover exchanges AUTHD_SERVICE_KEY for an authd-minted,
	// auto-refreshing service:runed token (auth.ServiceToken); authd's
	// issuer-pin would 401 the static RUNED_SERVICE_TOKEN. Fall back to the
	// static env when AUTHD_SERVICE_KEY is unset; no AUTHD_URL → no brokering
	// (local-dev).
	var broker runed.Broker
	if authdURL != "" {
		svcKey := os.Getenv("AUTHD_SERVICE_KEY")
		if ts, terr := auth.ServiceToken(authdURL, "runed", svcKey); terr == nil {
			broker = runed.NewHTTPBrokerWithSource(authdURL, ts.Token)
			slog.Info("runed service-token bootstrap via authd", "authd", authdURL)
		} else {
			// With a key set, a failed exchange means every per-spawn downscope
			// will 401 (the static fallback is not compose-provisioned in split) —
			// fail loud instead of silently brokering with an unusable token.
			if svcKey != "" {
				slog.Error("runed service-token exchange FAILED though AUTHD_SERVICE_KEY is set; "+
					"per-spawn token downscope will 401 — check authd reachability + key",
					"authd", authdURL, "err", terr)
			}
			broker = runed.NewHTTPBroker(authdURL, os.Getenv("RUNED_SERVICE_TOKEN"))
		}
	} else {
		broker = runed.NewStaticBroker("", "dev")
	}

	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	runtime := runed.NewDockerRuntime(cfg, folders)

	mgr := runed.NewManager(db, runtime, broker, runed.ManagerConfig{
		Scopes:        []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		RunTTL:        runTTL,
		Instance:      cfg.Name,
		MaxConcurrent: cfg.MaxContainers,
	})

	// hourly GC of expired spawns + tokens.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = db.SweepExpired(7 * 24 * time.Hour)
			}
		}
	}()

	// Startup parity with the former gateway.Run (spec 5/P): fail fast if the
	// container runtime is unreachable, then stop any orphaned spawn containers
	// left by a prior crash before we start accepting new runs.
	if err := container.EnsureRunning(); err != nil {
		slog.Error("container runtime check failed", "err", err)
		os.Exit(1)
	}
	container.CleanupOrphans(cfg.Name, cfg.Image)

	// Pre-seed each group's .codex/ (uid 1000) before any spawn so cold-start
	// parallel docker run can't materialize the bind source as root. Only when
	// the codex feature is enabled (HOST_CODEX_DIR set).
	if cfg.HostCodexDir != "" {
		container.SeedCodexDirs(cfg.GroupsDir)
	}

	srv := runed.NewServer(mgr, db, verify)
	mux := srv.Handler().(*http.ServeMux)
	// runed owns no manifest-addressable config rows (spec 5/36 catalog):
	// its tables are runtime (spawns / session_log / mcp_tokens). Empty
	// list → zero paths, but still emits the doc for aggregator uniformity.
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("runed", []string{}))

	httpd := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		slog.Info("runed started", "addr", listenAddr, "db", cfg.StoreDir)
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	// Graceful shutdown (spec 5/P § Graceful shutdown): stop accepting new
	// POST /v1/runs but DETACH in-flight runs — don't cancel them. Each
	// in-flight spawn's MCP socket lives inside its still-running
	// container.Run handler (driven by the per-request context, NOT the
	// daemon ctx), so the container can still call tools + submit_turn. We
	// wait up to RUNED_SHUTDOWN_GRACE for handlers to finish, then exit
	// (containers are docker --rm and outlive the daemon).
	grace := durOr("RUNED_SHUTDOWN_GRACE", runTTL)
	slog.Info("runed stopping (detaching in-flight runs)", "grace", grace, "in_flight", mgr.ActiveCount())
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durOr(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
