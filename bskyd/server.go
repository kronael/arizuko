package main

import (
	"context"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type creator interface {
	createPost(ctx context.Context, text, replyParentURI string) error
}

type botAdapter struct {
	chanlib.NoFileSender
	bc creator
}

func (a *botAdapter) Send(req chanlib.SendRequest) (string, error) {
	return "", a.bc.createPost(context.Background(), req.Content, req.ReplyTo)
}

func (a *botAdapter) Typing(string, bool) {}

type server struct {
	cfg config
	bc  creator
}

func newServer(cfg config, bc creator) *server { return &server{cfg: cfg, bc: bc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(
		s.cfg.Name, s.cfg.ChannelSecret, []string{"bluesky:"}, &botAdapter{bc: s.bc})
}
