package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs linkd implements. Forward/Quote/Dislike/Edit
// return honest Unsupported hints (no LinkedIn primitive / unwired UGC edit);
// SendFile (UGC media upload) is not wired. The cap↔impl consistency test
// guards this.
var caps = map[string]bool{
	"send_text":     true,
	"fetch_history": true,
	"post":          true,
	"like":          true,
	"delete":        true,
	"repost":        true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"linkedin:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			lc, err := newLinkClient(cfg)
			if err != nil {
				slog.Error("linkedin connect failed", "err", err)
				return nil, nil, err
			}
			go lc.poll(ctx, rc)
			return newServer(cfg, lc, lc.isConnected).handler(), nil, nil
		},
	})
}

// linkdStateDir is the DATA_DIR code default. It MUST equal the path
// template/services/linkd.yml mounts and exports, the same "set in both places
// so neither drifts" rule the :8080 LISTEN_ADDR convention follows (root
// CLAUDE.md). The old default was the containerDataMount ROOT, so a missing or
// typo'd env line dropped linkd-state-<name>.json — which holds the REFRESHED
// LinkedIn OAuth token — at the top of the instance tree, beside every daemon's
// database.
const linkdStateDir = "/srv/app/home/store/linkd"

type config struct {
	Name         string
	ClientID     string
	ClientSecret string
	AccessToken  string
	RefreshToken string
	RouterURL    string
	ListenAddr   string
	ListenURL    string
	DataDir      string
	APIBase      string
	OAuthBase    string
	PollInterval string
	AutoPublish  bool
}

func loadConfig() config {
	return config{
		Name:         chanlib.EnvOr("CHANNEL_NAME", "linkedin"),
		ClientID:     chanlib.MustEnv("LINKEDIN_CLIENT_ID"),
		ClientSecret: chanlib.MustEnv("LINKEDIN_CLIENT_SECRET"),
		AccessToken:  chanlib.EnvOr("LINKEDIN_ACCESS_TOKEN", ""),
		RefreshToken: chanlib.EnvOr("LINKEDIN_REFRESH_TOKEN", ""),
		RouterURL:    chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:   chanlib.EnvOr("LISTEN_ADDR", ":9010"),
		ListenURL:    chanlib.EnvOr("LISTEN_URL", "http://linkd:9010"),
		DataDir:      chanlib.EnvOr("DATA_DIR", linkdStateDir),
		APIBase:      chanlib.EnvOr("LINKEDIN_API_BASE", "https://api.linkedin.com"),
		OAuthBase:    chanlib.EnvOr("LINKEDIN_OAUTH_BASE", "https://www.linkedin.com"),
		PollInterval: chanlib.EnvOr("LINKEDIN_POLL_INTERVAL", "300s"),
		AutoPublish:  chanlib.EnvOr("LINKEDIN_AUTO_PUBLISH", "false") == "true",
	}
}
