package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var proxyClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg          config
	bc           chanlib.BotHandler
	maxFileBytes int64
}

func newServer(cfg config, bc chanlib.BotHandler) *server {
	return &server{cfg: cfg, bc: bc, maxFileBytes: cfg.MaxFileBytes}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"bluesky:"}, s.bc)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/files/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		chanlib.WriteErr(w, 400, "did and cid required")
		return
	}
	did, cid := parts[0], parts[1]

	// Build URL safely so attacker-controlled did/cid can't inject query params.
	q := url.Values{"did": {did}, "cid": {cid}}
	blobURL := s.cfg.Service + "/xrpc/com.atproto.sync.getBlob?" + q.Encode()

	resp, err := proxyClient.Get(blobURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "blob fetch failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "blob fetch failed")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	max := s.maxFileBytes
	if max <= 0 {
		max = 20 * 1024 * 1024
	}
	io.Copy(w, io.LimitReader(resp.Body, max))
}
