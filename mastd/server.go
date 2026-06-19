package main

import (
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

type server struct {
	cfg           config
	mc            chanlib.BotHandler
	resolve       func(string) (string, bool)
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, mc chanlib.BotHandler, resolve func(string) (string, bool), isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, mc: mc, resolve: resolve, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"mastodon:"}, s.mc, s.isConnected, s.lastInboundAt)
	mux.HandleFunc("GET /files/", chanlib.Auth(chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve:  s.resolve,
		MaxBytes: s.cfg.MediaMaxBytes,
	})))
	return mux
}
