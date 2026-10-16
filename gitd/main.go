package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// capsFor advertises send_text only when a token can post the comment, so a
// tokenless ingress-only gitd returns a clean Unsupported hint on `reply`
// instead of failing the call. Every other verb is Unsupported: repo state
// changes go through the GitHub MCP connector (spec 6/27 decision 2).
func capsFor(token string) map[string]bool {
	return map[string]bool{"send_text": token != ""}
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"github:"},
		Caps:       capsFor(cfg.Token),
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			// github_sources is routd's: gitd reads the sealed secret per
			// delivery and never writes — registration is routd's REST/MCP face
			// and the CLI. store.OpenOwner is the door.
			st, err := store.OpenOwner(store.OwnerRoutd)
			if err != nil {
				slog.Error("routd store open failed", "err", err)
				return nil, nil, err
			}
			keyring, err := core.RequireSecretKeyring(cfg.SecretsKey)
			if err != nil {
				slog.Error("secrets keyring", "err", err)
				return nil, nil, err
			}
			st.SetSecretKeys(keyring...)
			as, err := chanlib.OpenAdapterStore("gitd")
			if err != nil {
				slog.Error("adapter store open failed", "err", err)
				return nil, nil, err
			}
			// GitHub never retries a delivery on its own, so a routd that is
			// restarting must not lose the event: retain and re-deliver.
			rc.RetainInbound(ctx, as.Chan)
			b := newBot(cfg)
			verifyToken(ctx, b, rc)
			srv := newServer(cfg, b, st, as.Chan, rc)
			return srv.handler(), func() { b.stop(); _ = as.Close(); _ = st.Close() }, nil
		},
	})
}

// capSetter is the registration gitd revises when its token turns out dead.
type capSetter interface {
	SetCaps(map[string]bool) error
}

// verifyToken learns the token's login so ingest can drop gitd's own comments.
// A refusal is NOT fatal: chanlib.Run exits on a Start error, and a daemon
// that exits at boot cycles the whole compose project (BUGS L406), so one
// credential gating ONE verb would take every daemon on the instance down.
// gitd degrades to ingress-only and withdraws send_text: rc.Register already
// ran, so routd would otherwise keep routing replies to an adapter that
// cannot post them.
func verifyToken(ctx context.Context, b *bot, rc capSetter) {
	err := b.start(ctx)
	if err == nil {
		return
	}
	slog.Error("github token refused; gitd runs ingress-only, send_text is withdrawn, and gitd's own comments are no longer dropped at ingest",
		"err", err)
	if rerr := rc.SetCaps(capsFor("")); rerr != nil {
		slog.Error("withdrawing send_text failed; routd still routes replies to gitd", "err", rerr)
	}
}

type config struct {
	Name       string
	Token      string
	APIBase    string
	RouterURL  string
	ListenAddr string
	ListenURL  string
	SecretsKey string
}

func loadConfig() config {
	return config{
		Name:       chanlib.EnvOr("CHANNEL_NAME", "github"),
		Token:      chanlib.EnvOr("GITHUB_TOKEN", ""),
		APIBase:    chanlib.EnvOr("GITHUB_API_URL", "https://api.github.com"),
		RouterURL:  chanlib.MustEnv("ROUTER_URL"),
		ListenAddr: chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:  chanlib.EnvOr("LISTEN_URL", "http://gitd:8080"),
		SecretsKey: chanlib.EnvOr("SECRETS_KEY", ""),
	}
}
