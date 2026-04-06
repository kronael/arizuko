package main

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg   config
	rc    chanlib.BotHandler
	files *fileCache
}

func newServer(cfg config, rc chanlib.BotHandler, files *fileCache) *server {
	return &server{cfg: cfg, rc: rc, files: files}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"reddit:"}, s.rc)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/files/")
	if id == "" {
		chanlib.WriteErr(w, 400, "file_id required")
		return
	}
	rawURL, ok := s.files.Get(id)
	if !ok {
		chanlib.WriteErr(w, 404, "file not found")
		return
	}
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "download failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "download failed")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	io.Copy(w, resp.Body)
}
