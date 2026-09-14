package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/store"
)

const (
	accessTTL    = 15 * time.Minute
	refreshTTL   = 30 * 24 * time.Hour
	maxAccessTTL = time.Hour // bounds how long a retired key keeps verifying
)

func main() {
	defer obs.Setup("authd", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("authd", os.Getenv("ARIZUKO_INSTANCE"))()

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	// store.OwnerDriver is the door: it dials authd's tables on the instance's
	// store server, so authd holds no pool at all.
	d, closeStore, err := store.OwnerDriver(store.OwnerAuthd)
	if err != nil {
		slog.Error("open authd's store", "err", err)
		os.Exit(1)
	}
	defer closeStore() //nolint:errcheck // the process is ending
	if err := ensureStorage(d); err != nil {
		slog.Error("prepare storage", "err", err)
		os.Exit(1)
	}

	a, err := newAuthd(d, accessTTL, refreshTTL, maxAccessTTL)
	if err != nil {
		slog.Error("init signer", "err", err)
		os.Exit(1)
	}

	// authd verifies its OWN tokens, so the dashboard gate uses its local
	// keyset — there is no authd to fetch from, it IS authd.
	srv := &server{a: a, serviceSecrets: loadServiceSecrets(), dashKeys: a.LocalKeySet()}

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

	audit.Init(d, os.Getenv("ARIZUKO_INSTANCE"))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "daemons/authd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"store":        dynstore.URL(),
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

	slog.Info("authd started", "store", dynstore.URL(), "addr", listenAddr, "serving_keys", len(a.PublicKeys()))

	// cfg carries the OAuth provider config into mux(); nil means /auth/* stays
	// unmounted and human login 404s while health stays green, so the load
	// failure is surfaced here rather than swallowed.
	cfg, cerr := core.LoadConfig()
	if cerr != nil {
		slog.Error("oauth /auth/* not mounted: config load failed; human login will 404 while health stays green", "err", cerr)
		cfg = nil
	}
	httpd := &http.Server{Addr: listenAddr, Handler: obs.HTTPMiddleware("authd")(srv.mux(cfg))}

	// Register the signal handler BEFORE the listener accepts: a SIGTERM in
	// between takes Go's default disposition and kills the process, so the
	// Shutdown below never runs (BUGS L127).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpd.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

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
	for pair := range strings.SplitSeq(raw, ",") {
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
