package main

import (
	"context"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type poster interface {
	postStatus(ctx context.Context, text, replyToID string) error
}

type botAdapter struct {
	chanlib.NoFileSender
	mc poster
}

func (a *botAdapter) Send(req chanlib.SendRequest) (string, error) {
	return "", a.mc.postStatus(context.Background(), req.Content, req.ReplyTo)
}

func (a *botAdapter) Typing(string, bool) {}

type server struct {
	cfg config
	mc  poster
}

func newServer(cfg config, mc poster) *server { return &server{cfg: cfg, mc: mc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(
		s.cfg.Name, s.cfg.ChannelSecret, []string{"mastodon:"}, &botAdapter{mc: s.mc})
}
