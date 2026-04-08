package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/onvos/arizuko/api"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/gateway"
	"github.com/onvos/arizuko/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	slog.Info("gated starting",
		"poll_interval", cfg.PollInterval,
		"max_containers", cfg.MaxContainers,
		"idle_timeout", cfg.IdleTimeout,
		"onboarding", cfg.OnboardingEnabled,
	)

	s, err := store.Open(cfg.StoreDir)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	gw := gateway.New(cfg, s)

	reg := chanreg.New(cfg.ChannelSecret)
	apiSrv := api.New(reg, s)
	apiSrv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		slog.Info("channel registered", "name", name)
		gw.RemoveChannel(name)
		gw.AddChannel(ch)
		ch.DrainOutbox()
	})
	apiSrv.OnDeregister(func(name string) {
		slog.Info("channel deregistered", "name", name)
		gw.RemoveChannel(name)
	})

	addr := net.JoinHostPort("", strconv.Itoa(cfg.APIPort))
	srv := &http.Server{Addr: addr, Handler: apiSrv.Handler()}
	go func() {
		slog.Info("api server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("api server error", "err", err)
		}
	}()
	reg.StartHealthLoop(context.Background())

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := gw.Run(ctx); err != nil {
		slog.Error("gateway error", "err", err)
		os.Exit(1)
	}
	slog.Info("gated shutdown: flushing")
	gw.Shutdown()
	slog.Info("gated stopped")
}
