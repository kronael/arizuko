package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	bot chanlib.BotHandler
}

func newServer(cfg config, b chanlib.BotHandler) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"discord:"}, s.bot)
}
