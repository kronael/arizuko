package main

import (
	"context"
	"errors"
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
	// Spec 11/17-emaid-auth tier-3 DKIM is pre-wired but unimplemented. Surface the
	// gap loudly so an operator who set EMAIL_VERIFY_DKIM=true knows
	// they got A-R parsing only, not cryptographic re-verification.
	if cfg.Auth.VerifyDKIM {
		slog.Warn("EMAIL_VERIFY_DKIM=true but DKIM tier-3 not implemented; A-R parsing only (spec 11/17)")
	}
	if cfg.Auth.TrustedAuthserv == "" {
		slog.Warn("EMAIL_TRUSTED_AUTHSERV unset; every inbound classified as untrusted (fail-closed default per spec 11/17)")
	}
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"email:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			as, st, err := openStorage()
			if err != nil {
				slog.Error("store open failed", "err", err)
				return nil, nil, err
			}
			reg := newAttRegistry()
			p := newPoller(cfg, st, reg)
			go p.run(ctx, rc)
			return newServer(cfg, as.Chan, st, reg, p.isConnected, p.LastInboundAt).handler(),
				func() { as.Close() }, nil //nolint:errcheck // the process is ending
		},
	})
}

type config struct {
	Name          string
	IMAPHost      string
	SMTPHost      string
	Account       string
	Password      string
	AccessToken   string // EMAIL_ACCESS_TOKEN: XOAUTH2 instead of Password
	IMAPPort      string
	SMTPPort      string
	RouterURL     string
	ListenAddr    string
	ListenURL     string
	MaxAttachment int64
	Auth          AuthConfig
}

func loadConfig() config {
	c := config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "email"),
		IMAPHost:      chanlib.MustEnv("EMAIL_IMAP_HOST"),
		SMTPHost:      chanlib.MustEnv("EMAIL_SMTP_HOST"),
		Account:       chanlib.MustEnv("EMAIL_ACCOUNT"),
		Password:      os.Getenv("EMAIL_PASSWORD"),
		AccessToken:   os.Getenv("EMAIL_ACCESS_TOKEN"),
		IMAPPort:      chanlib.EnvOr("EMAIL_IMAP_PORT", "993"),
		SMTPPort:      chanlib.EnvOr("EMAIL_SMTP_PORT", "587"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9003"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://email:9003"),
		MaxAttachment: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		Auth:          LoadAuthConfig(os.Getenv),
	}
	if err := c.checkAuth(); err != nil {
		slog.Error("email auth misconfigured", "err", err)
		os.Exit(1)
	}
	return c
}

// checkAuth requires exactly one credential. The operator chooses the method
// by setting EMAIL_PASSWORD or EMAIL_ACCESS_TOKEN; both set is ambiguous and
// neither set is a mailbox with no way in, so each stops the boot rather than
// letting emaid pick a path the operator did not choose.
func (c config) checkAuth() error {
	switch {
	case c.Password != "" && c.AccessToken != "":
		return errors.New("EMAIL_PASSWORD and EMAIL_ACCESS_TOKEN are both set; set exactly one")
	case c.Password == "" && c.AccessToken == "":
		return errors.New("neither EMAIL_PASSWORD nor EMAIL_ACCESS_TOKEN is set; set exactly one")
	}
	return nil
}
