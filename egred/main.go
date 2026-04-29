package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()

	allow := NewAllowlist()
	proxy := NewProxy(allow)
	api := NewAPI(allow)

	proxySrv := proxy.Server()
	proxySrv.Addr = cfg.proxyAddr

	apiSrv := &http.Server{
		Addr:              cfg.apiAddr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("egred up", "proxy", cfg.proxyAddr, "api", cfg.apiAddr)

	go func() {
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("proxy serve", "err", err)
		}
	}()
	go func() {
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api serve", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("egred shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxySrv.Shutdown(ctx)
	apiSrv.Shutdown(ctx)
}

type config struct {
	proxyAddr string
	apiAddr   string
}

func loadConfig() config {
	return config{
		proxyAddr: envOr("EGRED_PROXY_ADDR", ":3128"),
		apiAddr:   envOr("EGRED_API_ADDR", ":3129"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
