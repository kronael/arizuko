package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs emaid implements. Email is immutable and
// not a feed, so delete/edit/forward/quote/repost/dislike and file/voice
// uploads return honest Unsupported hints. The cap↔impl consistency test
// guards this.
var caps = map[string]bool{"send_text": true, "fetch_history": true}

func main() {
	cfg := loadConfig()
	// Spec 10/17 tier-3 DKIM is pre-wired but unimplemented. Surface the
	// gap loudly so an operator who set EMAIL_VERIFY_DKIM=true knows
	// they got A-R parsing only, not cryptographic re-verification.
	if cfg.Auth.VerifyDKIM {
		slog.Warn("EMAIL_VERIFY_DKIM=true but DKIM tier-3 not implemented; A-R parsing only (spec 10/17)")
	}
	if cfg.Auth.TrustedAuthserv == "" {
		slog.Warn("EMAIL_TRUSTED_AUTHSERV unset; every inbound classified as untrusted (fail-closed default per spec 10/17)")
	}
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"email:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			db, err := openDB(cfg.DataDir)
			if err != nil {
				slog.Error("db open failed", "err", err)
				return nil, nil, err
			}
			reg := newAttRegistry()
			p := newPoller(cfg, db, reg)
			go p.run(ctx, rc)
			return newServer(cfg, db, reg, p.isConnected, p.LastInboundAt).handler(), func() { db.Close() }, nil
		},
	})
}

type config struct {
	Name          string
	IMAPHost      string
	SMTPHost      string
	Account       string
	Password      string
	IMAPPort      string
	SMTPPort      string
	RouterURL     string
	ListenAddr    string
	ListenURL     string
	DataDir       string
	MaxAttachment int64
	Auth          AuthConfig
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "email"),
		IMAPHost:      chanlib.MustEnv("EMAIL_IMAP_HOST"),
		SMTPHost:      chanlib.MustEnv("EMAIL_SMTP_HOST"),
		Account:       chanlib.MustEnv("EMAIL_ACCOUNT"),
		Password:      chanlib.MustEnv("EMAIL_PASSWORD"),
		IMAPPort:      chanlib.EnvOr("EMAIL_IMAP_PORT", "993"),
		SMTPPort:      chanlib.EnvOr("EMAIL_SMTP_PORT", "587"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9003"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://email:9003"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/emaid"),
		MaxAttachment: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		Auth:          LoadAuthConfig(os.Getenv),
	}
}
