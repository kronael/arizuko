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
	routdURL := envOr("ROUTD_URL", "http://routd:8080")
	svcToken := os.Getenv("RUNED_SERVICE_TOKEN")
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

	// Broker: downscope a capability token per spawn from authd. runed
	// mints nothing. Unset AUTHD_URL → no brokering (local-dev).
	var broker runed.Broker
	if authdURL != "" {
		broker = runed.NewHTTPBroker(authdURL, svcToken)
	} else {
		broker = runed.NewStaticBroker("", "dev")
	}

	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	fed := runed.NewFederator(routdURL)
	runtime := runed.NewDockerRuntime(cfg, folders, fed)

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

	srv := runed.NewServer(mgr, db, verify)
	mux := srv.Handler().(*http.ServeMux)
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("runed", nil))

	httpd := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		slog.Info("runed started", "addr", listenAddr, "db", cfg.StoreDir, "routd", routdURL)
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
