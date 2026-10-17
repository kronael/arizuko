package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// capsFor advertises send_text, typing and edit only when a token can post a
// comment, react to it and edit it; every other verb is Unsupported: repo state
// changes go through the GitHub MCP connector
// (specs/6/27-git-channel.md § Channel messages and repository tools). chanreg
// sends /typing to an adapter only when it advertises typing. edit is what lets
// routd keep one progress comment per turn (specs/6/46-turn-progress.md).
func capsFor(token string) map[string]bool {
	return map[string]bool{"send_text": token != "", "typing": token != "", "edit": token != ""}
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
			b.useMark(rc, as.Chan)
			if cfg.Token != "" {
				// A mark a crash left standing: find it by its record and clear it.
				go b.mark.Sweep(ctx)
			}
			verifyToken(ctx, b, rc, verifyEvery)
			srv := newServer(cfg, b, st, as.Chan, rc)
			return srv.handler(), func() { b.stop(); _ = as.Close(); _ = st.Close() }, nil
		},
	})
}

// capSetter is the registration gitd revises when its token turns out dead.
type capSetter interface {
	SetCaps(map[string]bool) error
}

// verifyEvery paces the re-ask of a /user GitHub never answered: one request a
// minute against a 5000/hour limit, and the first answer ends it.
const verifyEvery = time.Minute

// verifyToken learns the token's login so ingest can drop gitd's own comments.
// A failure is NOT fatal: chanlib.Run exits on a Start error, and a daemon
// that exits at boot cycles the whole compose project (BUGS L406), so one
// credential gating ONE verb would take every daemon on the instance down.
// gitd degrades to ingress-only and withdraws send_text: rc.Register already
// ran, so routd would otherwise keep routing replies to an adapter that
// cannot post them.
//
// GitHub being unreachable is not a refused token, and only the refusal is
// permanent. An unanswered /user leaves gitd without the login that drops its
// own comments at ingest, so send_text comes off until GitHub answers — and
// then goes back on, because nothing else in the process would ever ask again.
func verifyToken(ctx context.Context, b *bot, rc capSetter, every time.Duration) {
	err := b.start(ctx)
	if err == nil {
		return
	}
	slog.Error(noLoginConsequence("github /user failed")+
		"; gitd runs ingress-only, send_text is withdrawn, and gitd's own comments are no longer dropped at ingest",
		"err", err)
	setCaps(rc, capsFor(""))
	if errors.Is(err, chanlib.ErrPermanent) {
		return
	}
	go reverify(ctx, b, rc, every)
}

// reverify asks GitHub for the login until it answers, then puts send_text
// back. Only a failure that can repair itself gets here; a refusal ends it,
// because no retry turns a dead credential into a live one.
func reverify(ctx context.Context, b *bot, rc capSetter, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := b.start(ctx)
			switch {
			case err == nil:
				slog.Info("github /user answered; send_text is back on the registration", "login", b.Login())
				setCaps(rc, capsFor(b.cfg.Token))
				return
			case errors.Is(err, chanlib.ErrPermanent):
				slog.Error("github refused the token; send_text stays withdrawn until gitd restarts with a new one",
					"err", err)
				return
			default:
				slog.Warn("github /user still unanswered; send_text stays withdrawn", "err", err)
			}
		}
	}
}

func setCaps(rc capSetter, caps map[string]bool) {
	if err := rc.SetCaps(caps); err != nil {
		slog.Error("revising the registration failed; routd's view of gitd's verbs is stale",
			"caps", caps, "err", err)
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
