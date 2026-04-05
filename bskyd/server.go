package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	bc  chanlib.BotHandler
}

func newServer(cfg config, bc chanlib.BotHandler) *server { return &server{cfg: cfg, bc: bc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"bluesky:"}, s.bc)
}
