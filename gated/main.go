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

	"github.com/kronael/arizuko/api"
	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/gateway"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/store"
)

func main() {
	defer obs.Setup("gated", os.Getenv("ARIZUKO_INSTANCE"))()

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.EgressAPI != "" {
		if cfg.EgressNetworkPrefix == "" || cfg.EgressCrackbox == "" {
			slog.Error("config",
				"err", "egress on (CRACKBOX_ADMIN_API set) requires EGRESS_NETWORK_PREFIX + EGRESS_CRACKBOX (compose generation should set them)")
			os.Exit(1)
		}
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

	// Secrets are encrypted at rest — SECRETS_KEY is required (no AUTH_SECRET
	// fallback: poor key separation, and an always-set fallback is what made the
	// old purge wipe every plaintext secret on each startup). Comma-separate to
	// rotate: the first is the active seal key, the rest decrypt-only. On boot,
	// migrate any plaintext rows in place under the active key; never delete
	// (spec 6/Y).
	keyring := core.SecretKeyring(cfg.SecretsKey)
	if len(keyring) == 0 {
		slog.Error("SECRETS_KEY required: secrets are encrypted at rest; set a strong value (comma-separate to retain retired keys during rotation)")
		os.Exit(1)
	}
	s.SetSecretKeys(keyring...)
	if migErr := s.EncryptPlaintextSecrets(context.Background()); migErr != nil {
		slog.Error("secrets encrypt-at-rest migrate", "err", migErr)
		os.Exit(1)
	}

	gw := gateway.New(cfg, s)

	// Reuse the live channel per adapter so api.handleOutbound preserves the
	// retry outbox instead of constructing a throwaway channel per request.
	var (
		chanMu       sync.RWMutex
		httpChannels = map[string]*chanreg.HTTPChannel{}
	)

	reg := chanreg.New(cfg.ChannelSecret)
	apiSrv := api.New(reg, s)
	apiSrv.SetEngagementTTL(cfg.EngagementTTL)
	apiSrv.SetRouteTokenFns(gw.GatedFns(), os.Getenv("PROXYD_HMAC_SECRET"))
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

	auditCfg := audit.LoadConfig(cfg.HostProjectRoot, cfg.Name)
	aud := audit.New(auditCfg)
	gw.SetAudit(aud)
	aud.StartPoll(ctx, s.DB())

	audit.Init(s.DB(), cfg.Name)
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "daemons/gated",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"poll_interval":  cfg.PollInterval.String(),
			"max_containers": cfg.MaxContainers,
		},
	})

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
