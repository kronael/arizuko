package main

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var proxyClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg config
	bc  chanlib.BotHandler
}

func newServer(cfg config, bc chanlib.BotHandler) *server { return &server{cfg: cfg, bc: bc} }

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"bluesky:"}, s.bc)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	// Path: /files/{did}/{cid}
	p := strings.TrimPrefix(r.URL.Path, "/files/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		chanlib.WriteErr(w, 400, "did and cid required")
		return
	}
	did, cid := parts[0], parts[1]

	blobURL := s.cfg.Service + "/xrpc/com.atproto.sync.getBlob?did=" + did + "&cid=" + cid
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
	io.Copy(w, resp.Body)
}
