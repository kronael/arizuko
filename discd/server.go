package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg           config
	bot           chanlib.BotHandler
	files         *chanlib.URLCache
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, b chanlib.BotHandler, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, bot: b, files: chanlib.NewURLCache(0), isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"discord:"}, s.bot, s.isConnected, s.lastInboundAt)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/files/")
	if id == "" {
		chanlib.WriteErr(w, 400, "file id required")
		return
	}
	cdnURL, ok := s.files.Get(id)
	if !ok {
		chanlib.WriteErr(w, 404, "not found")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", cdnURL, nil)
	if err != nil {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	req.Header.Set("User-Agent", chanlib.UserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	defer resp.Body.Close()
	chanlib.ProxyFile(w, resp, s.cfg.MediaMaxBytes)
}
