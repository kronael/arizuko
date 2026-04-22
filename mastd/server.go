package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var proxyClient = &http.Client{Timeout: 30 * time.Second}

type fileResolver interface {
	FileURL(id string) (string, bool)
}

type server struct {
	cfg         config
	mc          chanlib.BotHandler
	files       fileResolver
	isConnected func() bool
}

func newServer(cfg config, mc chanlib.BotHandler, fr fileResolver, isConnected func() bool) *server {
	return &server{cfg: cfg, mc: mc, files: fr, isConnected: isConnected}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"mastodon:"}, s.mc, s.isConnected)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/files/")
	if id == "" {
		chanlib.WriteErr(w, 400, "attachment id required")
		return
	}
	cdnURL, ok := s.files.FileURL(id)
	if !ok {
		chanlib.WriteErr(w, 404, "attachment not found")
		return
	}
	resp, err := proxyClient.Get(cdnURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	defer resp.Body.Close()
	chanlib.ProxyFile(w, resp, s.cfg.MaxFileBytes)
}
