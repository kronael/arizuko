package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	_ "modernc.org/sqlite"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/store"
)

type config struct {
	listenAddr    string
	listenURL     string
	routerURL     string
	proxydURL     string
	channelSecret string
	hmacSecret    string
	storeDir      string
	assistantName string
	authdURL      string // soak: dual-accept ES256 bearers alongside HMAC; unset → HMAC-only
}

func loadConfig() config {
	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	return config{
		listenAddr:    chanlib.EnvOr("WEBD_LISTEN", ":8080"),
		listenURL:     chanlib.EnvOr("WEBD_URL", "http://webd:8080"),
		routerURL:     chanlib.EnvOr("ROUTER_URL", "http://gated:8080"),
		proxydURL:     chanlib.EnvOr("PROXYD_URL", "http://proxyd:8080"),
		channelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		hmacSecret:    loadHMACSecret(),
		storeDir:      coreCfg.StoreDir,
		assistantName: chanlib.EnvOr("ASSISTANT_NAME", "assistant"),
		authdURL:      strings.TrimRight(os.Getenv("AUTHD_URL"), "/"),
	}
}

func main() {
	defer obs.Setup("webd", os.Getenv("ARIZUKO_INSTANCE"))()

	cfg := loadConfig()

	st, err := store.Open(cfg.storeDir)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	audit.Init(st.DB(), os.Getenv("ARIZUKO_INSTANCE"))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/webd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"addr": cfg.listenAddr,
		},
	})

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Soak (spec 5/1 § cutover): when AUTHD_URL is set, build a JWKs KeySet so
	// requireUser/-Folder additionally accept an authd-minted ES256 bearer
	// alongside the live HMAC X-User-Sig. Unset → nil KeySet → HMAC-only,
	// exactly as before.
	var ks *auth.KeySet
	if cfg.authdURL != "" {
		var kerr error
		if ks, kerr = auth.FetchKeys(ctx, cfg.authdURL); kerr != nil {
			slog.Error("fetch authd keys", "err", kerr)
			os.Exit(1)
		}
	}

	hub := newHub()

	rc := chanlib.NewRouterClient(cfg.routerURL, cfg.channelSecret)
	_, err = rc.Register("web", cfg.listenURL,
		[]string{"web:"}, map[string]bool{"send_text": true, "typing": true},
	)
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("registered with router", "url", cfg.routerURL)

	ln, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.listenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("webd starting", "addr", cfg.listenAddr)

	srv := &http.Server{Handler: newServer(cfg, st, hub, rc, ks).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.Deregister()
	srv.Close()
}
