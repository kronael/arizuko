package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

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
	if cfg.EgressEnabled {
		if cfg.EgressNetworkPrefix == "" || cfg.EgressCrackbox == "" {
			slog.Error("config",
				"err", "EGRESS_ISOLATION=true requires EGRESS_NETWORK_PREFIX and EGRESS_CRACKBOX (compose generation should set them)")
			os.Exit(1)
		}
	}
	slog.Info("gated starting",
		"poll_interval", cfg.PollInterval,
		"max_containers", cfg.MaxContainers,
		"idle_timeout", cfg.IdleTimeout,
		"onboarding", cfg.OnboardingEnabled,
	)

	var (
		s *store.Store
	)
	if cfg.AuthSecret == "" {
		slog.Warn("AUTH_SECRET unset; secrets API disabled (folder/user secrets will not be injected at spawn)")
		s, err = store.Open(cfg.StoreDir)
	} else {
		s, err = store.OpenWithSecret(cfg.StoreDir, cfg.AuthSecret)
	}
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	gw := gateway.New(cfg, s)

	// Reuse the live channel per adapter so api.handleOutbound preserves the
	// retry outbox instead of constructing a throwaway channel per request.
	var (
		chanMu       sync.RWMutex
		httpChannels = map[string]*chanreg.HTTPChannel{}
	)

	reg := chanreg.New(cfg.ChannelSecret)
	apiSrv := api.New(reg, s)
	apiSrv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		gw.RemoveChannel(name)
		gw.AddChannel(ch)
		chanMu.Lock()
		httpChannels[name] = ch
		chanMu.Unlock()
		ch.DrainOutbox()
	})
	apiSrv.OnDeregister(func(name string) {
		gw.RemoveChannel(name)
		chanMu.Lock()
		delete(httpChannels, name)
		chanMu.Unlock()
	})
	apiSrv.ChannelLookup(func(name string) *chanreg.HTTPChannel {
		chanMu.RLock()
		defer chanMu.RUnlock()
		return httpChannels[name]
	})

	addr := net.JoinHostPort("", strconv.Itoa(cfg.APIPort))
	srv := &http.Server{
		Addr:              addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("api server starting", "addr", addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api server error", "err", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	reg.StartHealthLoop(ctx)

	go func() {
		<-ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Error("api server shutdown", "err", err)
		}
	}()

	if err := gw.Run(ctx); err != nil {
		slog.Error("gateway error", "err", err)
		os.Exit(1)
	}
	slog.Info("gated shutdown: flushing")
	gw.Shutdown()
	slog.Info("gated stopped")
}
