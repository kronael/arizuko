package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	mc  chanlib.BotHandler
}

func newServer(cfg config, mc chanlib.BotHandler) *server { return &server{cfg: cfg, mc: mc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"mastodon:"}, s.mc)
}
