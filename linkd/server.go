package main

import (
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
)

type server struct {
	cfg         config
	bot         chanlib.BotHandler
	chanStore   *chanstore.Store
	isConnected func() bool
}

func newServer(cfg config, bot chanlib.BotHandler, chanStore *chanstore.Store, isConnected func() bool) *server {
	return &server{cfg: cfg, bot: bot, chanStore: chanStore, isConnected: isConnected}
}

func (s *server) handler() http.Handler {
	// linkd is a stub adapter with no inbound plumbing; report "now" so
	// /health never flips stale while auth is valid.
	lastInbound := func() int64 { return time.Now().Unix() }
	return chanlib.NewAdapterMux(s.cfg.Name, []string{"linkedin:"}, s.bot, s.isConnected, lastInbound, s.chanStore)
}
