package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	rc  chanlib.BotHandler
}

func newServer(cfg config, rc chanlib.BotHandler) *server { return &server{cfg: cfg, rc: rc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"reddit:"}, s.rc)
}
