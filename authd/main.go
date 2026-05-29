package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
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

	dsn := os.Getenv("DATABASE")
	if dsn == "" {
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			slog.Error("DATABASE or DATA_DIR env required")
			os.Exit(1)
		}
		dsn = filepath.Join(dataDir, "store", "messages.db")
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
		if _, err := a.MintForSubject("service:authd", nil, serviceGrants["service:authd"], ""); err != nil {
			slog.Error("self-mint service:authd", "err", err)
			os.Exit(1)
		}
		slog.Info("self-minted service:authd")
	}

	slog.Info("authd started", "db", dsn, "addr", listenAddr, "serving_keys", len(a.PublicKeys()))

	mux := srv.mux()
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("authd", []string{}))
	httpd := &http.Server{Addr: listenAddr, Handler: mux}

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
