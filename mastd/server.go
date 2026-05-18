package main

import (
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

type fileResolver interface {
	FileURL(id string) (string, bool)
}

type server struct {
	cfg           config
	mc            chanlib.BotHandler
	files         fileResolver
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, mc chanlib.BotHandler, fr fileResolver, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, mc: mc, files: fr, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"mastodon:"}, s.mc, s.isConnected, s.lastInboundAt)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve:  s.files.FileURL,
		MaxBytes: s.cfg.MaxFileBytes,
	})))
	return mux
}
