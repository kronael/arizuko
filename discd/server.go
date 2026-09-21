package main

import (
	"net/http"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
)

type server struct {
	cfg           config
	bot           chanlib.BotHandler
	files         *chanlib.URLCache
	chanStore     *chanstore.Store
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, b chanlib.BotHandler, chanStore *chanstore.Store, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, bot: b, files: chanlib.NewURLCache(0, chanStore), chanStore: chanStore, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"discord:"}, s.bot, s.isConnected, s.lastInboundAt, s.chanStore)
	mux.HandleFunc("GET /files/", chanlib.Auth(chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve:  s.files.Get,
		MaxBytes: s.cfg.MediaMaxBytes,
	})))
	return mux
}
