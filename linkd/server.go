package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg         config
	bot         chanlib.BotHandler
	isConnected func() bool
}

func newServer(cfg config, bot chanlib.BotHandler, isConnected func() bool) *server {
	return &server{cfg: cfg, bot: bot, isConnected: isConnected}
}

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"linkedin:"}, s.bot, s.isConnected)
}
