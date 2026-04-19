package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:          cfg.Name,
		RouterURL:     cfg.RouterURL,
		ChannelSecret: cfg.ChannelSecret,
		ListenAddr:    cfg.ListenAddr,
		ListenURL:     cfg.ListenURL,
		Prefixes:      []string{"linkedin:"},
		Caps:          map[string]bool{"send_text": true},
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			lc, err := newLinkClient(cfg)
			if err != nil {
				slog.Error("linkedin connect failed", "err", err)
				return nil, nil, err
			}
			go lc.poll(ctx, rc)
			return newServer(cfg, lc).handler(), nil, nil
		},
	})
}

type config struct {
	Name          string
	ClientID      string
	ClientSecret  string
	AccessToken   string
	RefreshToken  string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	DataDir       string
	APIBase       string
	OAuthBase     string
	PollInterval  string
	AutoPublish   bool
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "linkedin"),
		ClientID:      chanlib.MustEnv("LINKEDIN_CLIENT_ID"),
		ClientSecret:  chanlib.MustEnv("LINKEDIN_CLIENT_SECRET"),
		AccessToken:   chanlib.EnvOr("LINKEDIN_ACCESS_TOKEN", ""),
		RefreshToken:  chanlib.EnvOr("LINKEDIN_REFRESH_TOKEN", ""),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9006"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://linkd:9006"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/app/home"),
		APIBase:       chanlib.EnvOr("LINKEDIN_API_BASE", "https://api.linkedin.com"),
		OAuthBase:     chanlib.EnvOr("LINKEDIN_OAUTH_BASE", "https://www.linkedin.com"),
		PollInterval:  chanlib.EnvOr("LINKEDIN_POLL_INTERVAL", "300s"),
		AutoPublish:   chanlib.EnvOr("LINKEDIN_AUTO_PUBLISH", "false") == "true",
	}
}
