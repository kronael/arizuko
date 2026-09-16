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
			if err := b.start(ctx); err != nil {
				slog.Error("github token check failed", "err", err)
				return nil, nil, err
			}
			srv := newServer(cfg, b, st, as.Chan, rc)
			return srv.handler(), func() { b.stop(); as.Close(); st.Close() }, nil
		},
	})
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
