package main

import (
	"database/sql"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

type server struct {
	cfg           config
	bot           chanlib.BotHandler
	files         *chanlib.URLCache
	db            *sql.DB
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, b chanlib.BotHandler, db *sql.DB, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, bot: b, files: chanlib.NewURLCache(0), db: db, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"discord:"}, s.bot, s.isConnected, s.lastInboundAt, s.db)
	mux.HandleFunc("GET /files/", chanlib.Auth(chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve:  s.files.Get,
		MaxBytes: s.cfg.MediaMaxBytes,
	})))
	return mux
}
