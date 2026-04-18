package main

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

const fileCacheCapacity = 4096

type server struct {
	cfg   config
	bot   chanlib.BotHandler
	files fileCache
}

// fileCache is a bounded LRU mapping short opaque ids to CDN URLs.
// Entries are evicted in least-recently-used order once capacity is
// reached so long-running adapters don't leak.
type fileCache struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	order *list.List
	cap   int
}

type fileEntry struct {
	id, url string
}

func (fc *fileCache) init() {
	if fc.m == nil {
		fc.m = map[string]*list.Element{}
		fc.order = list.New()
		if fc.cap == 0 {
			fc.cap = fileCacheCapacity
		}
	}
}

func (fc *fileCache) Put(url string) string {
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))[:12]
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.init()
	if el, ok := fc.m[id]; ok {
		fc.order.MoveToFront(el)
		return id
	}
	el := fc.order.PushFront(&fileEntry{id: id, url: url})
	fc.m[id] = el
	for fc.order.Len() > fc.cap {
		back := fc.order.Back()
		if back == nil {
			break
		}
		fc.order.Remove(back)
		delete(fc.m, back.Value.(*fileEntry).id)
	}
	return id
}

func (fc *fileCache) Get(id string) (string, bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.init()
	el, ok := fc.m[id]
	if !ok {
		return "", false
	}
	fc.order.MoveToFront(el)
	return el.Value.(*fileEntry).url, true
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
