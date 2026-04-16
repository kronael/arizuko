package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg   config
	bot   chanlib.BotHandler
	files fileCache
}

type fileCache struct {
	mu sync.RWMutex
	m  map[string]string
}

func (fc *fileCache) Put(url string) string {
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))[:12]
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.m == nil {
		fc.m = make(map[string]string)
	}
	fc.m[h] = url
	return h
}

func (fc *fileCache) Get(id string) (string, bool) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	u, ok := fc.m[id]
	return u, ok
}

func newServer(cfg config, b chanlib.BotHandler) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"discord:"}, s.bot)
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
	resp, err := httpClient.Get(cdnURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	io.Copy(w, resp.Body)
}
