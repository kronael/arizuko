package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "modernc.org/sqlite"
)

const (
	accessTTL    = 15 * time.Minute
	refreshTTL   = 30 * 24 * time.Hour
	maxAccessTTL = time.Hour // bounds how long a retired key keeps verifying
)

func main() {
	defer obs.Setup("authd", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("authd", os.Getenv("ARIZUKO_INSTANCE"))()

	dsn, err := resolveDSN(os.Getenv("DATABASE"), os.Getenv("DATA_DIR"))
	if err != nil {
		slog.Error("resolve db path", "err", err)
		os.Exit(1)
	}
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		slog.Warn("set WAL mode", "err", err)
	}
	if err := migrate(db); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	a, err := newAuthd(db, accessTTL, refreshTTL, maxAccessTTL)
	if err != nil {
		slog.Error("init signer", "err", err)
		os.Exit(1)
	}

	srv := &server{a: a, serviceSecrets: loadServiceSecrets()}

	// Wire the grants fetcher onto BOTH the daemon (refresh re-snapshot) and the
	// server (login snapshot + issuer-mint ceiling). GRANTS_URL unset → grants
	// stays nil (every session empty-scope; current behavior).
	if grantsURL := os.Getenv("GRANTS_URL"); grantsURL != "" {
		g := newHTTPGrants(a, grantsURL)
		srv.grants = g
		a.grants = g
		slog.Info("grants fetcher wired", "url", grantsURL)
	} else {
		slog.Info("GRANTS_URL unset: grants unwired, sessions are empty-scope")
	}

	audit.Init(db, os.Getenv("ARIZUKO_INSTANCE"))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "daemons/authd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"dsn":          dsn,
			"serving_keys": len(a.PublicKeys()),
			"service_subs": len(srv.serviceSecrets),
		},
	})

	// Self-mint service:authd at boot from AUTHD_SERVICE_KEY, proving the
	// signer is live and giving authd its own identity for outbound calls.
	if boot := os.Getenv("AUTHD_SERVICE_KEY"); boot != "" {
		if _, err := a.MintForSubject("service:authd", "service", nil, serviceGrants["service:authd"], ""); err != nil {
			slog.Error("self-mint service:authd", "err", err)
			os.Exit(1)
		}
		slog.Info("self-minted service:authd")
	}

	slog.Info("authd started", "db", dsn, "addr", listenAddr, "serving_keys", len(a.PublicKeys()))

	mux := srv.mux()
	// OAuth /auth/* (spec 5/1): authd is the OAuth provider, minting ES256.
	// Mounted only when provider config is present (AUTH_BASE_URL + a client id).
	if cfg, cerr := core.LoadConfig(); cerr == nil {
		srv.secureCookies = strings.HasPrefix(auth.AuthBaseURL(cfg), "https://")
		srv.registerOAuth(mux, cfg)
	} else {
		slog.Warn("oauth /auth/* not mounted: config load failed", "err", cerr)
	}
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("authd", []string{}))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	httpd := &http.Server{Addr: listenAddr, Handler: obs.HTTPMiddleware("authd")(mux)}

	go func() {
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("authd stopping")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpd.Shutdown(ctx)
}

// resolveDSN picks authd's SQLite path. An explicit DATABASE wins; otherwise
// the DB is <DATA_DIR>/store/auth.db — under store/ alongside routd.db /
// runed.db / messages.db so a single `store/` chown to the container uid makes
// every daemon's DB writable on a fresh root-owned data dir. authd owns auth.db
// and runs its own migrations; it must NOT migrate gated's messages.db
// (CLAUDE.md DB-ownership rule).
func resolveDSN(database, dataDir string) (string, error) {
	if database != "" {
		return database, nil
	}
	if dataDir == "" {
		return "", errMissingDB
	}
	storeDir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(storeDir, "auth.db"), nil
}

var errMissingDB = errors.New("DATABASE or DATA_DIR env required")

// loadServiceSecrets parses AUTHD_SERVICE_KEYS — a comma-separated list of
// `principal=secret` pairs — into secret→principal. Compose generation writes
// each daemon's own secret into that daemon's env and the full set here.
func loadServiceSecrets() map[string]string {
	out := map[string]string{}
	raw := os.Getenv("AUTHD_SERVICE_KEYS")
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		i := strings.IndexByte(pair, '=')
		if i <= 0 || i == len(pair)-1 {
			continue
		}
		out[pair[i+1:]] = pair[:i]
	}
	// authd's own bootstrap doubles as a service secret so it can exchange too.
	if boot := os.Getenv("AUTHD_SERVICE_KEY"); boot != "" {
		out[boot] = "service:authd"
	}
	return out
}
