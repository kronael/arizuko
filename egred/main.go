package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
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

	plis, err := net.Listen("tcp", cfg.proxyAddr)
	if err != nil {
		slog.Error("proxy listen", "addr", cfg.proxyAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("egred up",
		"proxy", cfg.proxyAddr,
		"api", cfg.apiAddr,
	)

	go func() {
		if err := proxy.Serve(plis); err != nil {
			slog.Error("proxy serve", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.apiAddr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api serve", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("egred shutting down")
	plis.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	proxy.Wait()
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
