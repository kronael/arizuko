// Command routd runs the routd daemon: the conversation state machine —
// routing rules + the message/event store + the orchestration loop +
// channel ingress/egress. routd is the SOLE appender of messages and a
// token VERIFIER, not a signer (spec 5/E). It owns routd.db and calls
// runed (POST /v1/runs) to execute turns.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/routd"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

func main() {
	defer obs.Setup("routd", os.Getenv("ARIZUKO_INSTANCE"))()

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		slog.Error("DATA_DIR env required")
		os.Exit(1)
	}
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	authdURL := os.Getenv("AUTHD_URL")
	runedURL := envOr("RUNED_URL", "http://runed:8080")
	webHost := os.Getenv("WEB_HOST")

	db, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		slog.Error("open routd.db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verifier: offline-verify bearer tokens against authd's JWKs. routd
	// holds no signing key (spec 5/E § Auth). Unset AUTHD_URL → no
	// verifier (single-tenant / local-dev); the Server treats nil as open.
	var verify routd.Verifier
	if authdURL != "" {
		ks, kerr := auth.FetchKeys(ctx, authdURL)
		if kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
		verify = keysetVerifier{ks: ks}
	}

	// runed client: routd's service token authenticates POST /v1/runs. The
	// HMAC→ES256 cutover exchanges AUTHD_SERVICE_KEY for an authd-minted
	// service:routd ES256 token at boot and refreshes it (auth.ServiceToken);
	// authd's issuer-pin would 401 the static ROUTD_SERVICE_TOKEN. When
	// AUTHD_URL or AUTHD_SERVICE_KEY is unset, fall back to the static env
	// (additive, local-dev safe).
	runTimeout := durOr("RUNED_RUN_TIMEOUT", 20*time.Minute)
	var runedClient *runedv1.Client
	if ts, err := auth.ServiceToken(authdURL, "routd", os.Getenv("AUTHD_SERVICE_KEY")); err == nil {
		runedClient = runedv1.NewClientWithSource(runedURL, ts.Token, runTimeout)
		slog.Info("routd service-token bootstrap via authd", "authd", authdURL)
	} else {
		runedClient = runedv1.NewClient(runedURL, os.Getenv("ROUTD_SERVICE_TOKEN"), runTimeout)
	}

	// Channel plane (ported from gated): adapters register their egress URL +
	// owned jid prefixes; the Deliverer resolves them on the way out using the
	// same order gated used (latest inbound source → registry prefix match).
	// In-memory registry — adapters re-register on routd restart.
	reg := chanreg.New(os.Getenv("CHANNEL_SECRET"))
	deliver, onRegister, onDeregister := routd.NewChannelDeliverer(
		reg, parseCSV(os.Getenv("SEND_DISABLED_CHANNELS")), db.LatestSource)

	loop := routd.NewLoop(db, runedClient, routd.LoopConfig{
		RunTimeout: runTimeout,
		IpcDir:     filepath.Join(dataDir, "ipc"),
		RunScopes: []types.Scope{
			"messages:send:own_group", "chats:read:own_group",
		},
		Deliver:   deliver,
		Proactive: routd.LoadProactiveConfig(os.Getenv),
		GroupsDir: filepath.Join(dataDir, "groups"),
		// Prompt envelope (buildAgentPrompt). Defaults mirror core.LoadConfig.
		InstanceName:          envOr("ASSISTANT_NAME", "Andy"),
		ObserveWindowMessages: intOr("OBSERVE_WINDOW_MESSAGES", 10),
		ObserveWindowChars:    intOr("OBSERVE_WINDOW_CHARS", 4000),
	})

	srv := routd.NewServer(db, loop, deliver, verify, durOr("ENGAGEMENT_TTL", 30*time.Minute), webHost)
	srv.SetChannelRegistry(reg, onRegister, onDeregister)
	reg.StartHealthLoop(ctx)
	mux := srv.Handler().(*http.ServeMux)
	// routd owns the residual config + conversation tables per spec 5/36
	// resource catalog (inherits gated's schema authority).
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("routd", []string{
		"groups", "routes", "web_routes", "acl", "acl_membership", "secrets", "network_rules",
	}))

	httpd := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		slog.Info("routd started", "addr", listenAddr, "db", dataDir, "runed", runedURL)
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()
	go loop.Run(ctx)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("routd stopping")
	cancel()
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = httpd.Shutdown(sctx)
}

// keysetVerifier adapts auth.FetchKeys → routd.Verifier (offline JWT
// verify). routd is a verifier, not a signer.
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

func intOr(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
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

// parseCSV splits a comma-separated env value into trimmed, non-empty
// entries (SEND_DISABLED_CHANNELS), mirroring core.parseCSV.
func parseCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
