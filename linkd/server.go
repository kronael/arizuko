package main

import (
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
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
	// linkd is a stub adapter with no inbound plumbing; report "now" so
	// /health never flips stale while auth is valid.
	lastInbound := func() int64 { return time.Now().Unix() }
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"linkedin:"}, s.bot, s.isConnected, lastInbound)
}
