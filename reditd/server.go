package main

import (
	"database/sql"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/kronael/arizuko/chanlib"
)

var allowedRedditHosts = map[string]bool{
	"i.redd.it":                true,
	"v.redd.it":                true,
	"preview.redd.it":          true,
	"external-preview.redd.it": true,
	"i.imgur.com":              true,
	"imgur.com":                true,
}

type server struct {
	cfg           config
	rc            chanlib.BotHandler
	files         *chanlib.URLCache
	db            *sql.DB
	safeFetch     func(string) bool
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, rc chanlib.BotHandler, files *chanlib.URLCache, db *sql.DB, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, rc: rc, files: files, db: db, safeFetch: isSafeFetchURL, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"reddit:"}, s.rc, s.isConnected, s.lastInboundAt, s.db)
	mux.HandleFunc("GET /files/", chanlib.Auth(chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve:  s.files.Get,
		Validate: s.safeFetch,
		MaxBytes: s.cfg.MediaMaxBytes,
	})))
	return mux
}

func isSafeFetchURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if !allowedRedditHosts[strings.ToLower(host)] {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
	}
	return true
}
