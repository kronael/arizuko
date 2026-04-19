package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	bot chanlib.BotHandler
}

func newServer(cfg config, bot chanlib.BotHandler) *server {
	return &server{cfg: cfg, bot: bot}
}

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"linkedin:"}, s.bot)
}
