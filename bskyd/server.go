package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

var proxyClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg           config
	bc            chanlib.BotHandler
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, bc chanlib.BotHandler, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, bc: bc, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"bluesky:"}, s.bc, s.isConnected, s.lastInboundAt)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/files/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		chanlib.WriteErr(w, 400, "did and cid required")
		return
	}
	q := url.Values{"did": {parts[0]}, "cid": {parts[1]}}
	resp, err := proxyClient.Get(s.cfg.Service + "/xrpc/com.atproto.sync.getBlob?" + q.Encode())
	if err != nil {
		chanlib.WriteErr(w, 502, "blob fetch failed")
		return
	}
	defer resp.Body.Close()
	chanlib.ProxyFile(w, resp, s.cfg.MaxFileBytes)
}
