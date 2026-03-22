package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	_ "modernc.org/sqlite"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type config struct {
	listenAddr    string
	listenURL     string
	routerURL     string
	channelSecret string
	authSecret    string
	storeDir      string
	assistantName string
}

func loadConfig() config {
	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	return config{
		listenAddr:    chanlib.EnvOr("WEBD_LISTEN", ":9002"),
		listenURL:     chanlib.MustEnv("WEBD_URL"),
		routerURL:     chanlib.MustEnv("ROUTER_URL"),
		channelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		authSecret:    coreCfg.AuthSecret,
		storeDir:      coreCfg.StoreDir,
		assistantName: chanlib.EnvOr("ASSISTANT_NAME", "assistant"),
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()

	st, err := store.Open(cfg.storeDir)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	hub := newHub()

	rc := chanlib.NewRouterClient(cfg.routerURL, cfg.channelSecret)
	token, err := rc.Register("web", cfg.listenURL,
		[]string{"web:"}, map[string]bool{"send_text": true, "typing": true},
	)
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.Token = token
	slog.Info("registered with router", "url", cfg.routerURL)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	ln, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.listenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("webd starting", "addr", cfg.listenAddr)

	srv := &http.Server{Handler: newServer(cfg, st, hub, rc).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.Deregister()
	srv.Close()
}
