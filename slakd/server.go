package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

const maxEventBody = 1 << 20 // 1 MiB — Slack events are small

var httpClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg           config
	bot           *bot
	files         *chanlib.URLCache
	isConnected   func() bool
	lastInboundAt func() int64
	now           func() time.Time
}

func newServer(cfg config, b *bot, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{
		cfg:           cfg,
		bot:           b,
		files:         chanlib.NewURLCache(0),
		isConnected:   isConnected,
		lastInboundAt: lastInboundAt,
		now:           time.Now,
	}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"slack:"}, s.bot, s.isConnected, s.lastInboundAt)
	// Slack Events webhook — verified via signing secret; no chanlib.Auth.
	mux.HandleFunc("POST /slack/events", s.handleEvents)
	// File proxy — chanlib.Auth-protected; adds Bearer xoxb upstream.
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

// handleEvents verifies the Slack signature and dispatches the body. proxyd
// must pass body bytes + headers verbatim; we re-read body before verifying,
// so any re-marshal upstream breaks the HMAC.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBody))
	if err != nil {
		chanlib.WriteErr(w, 400, "body read failed")
		return
	}
	sig := r.Header.Get("X-Slack-Signature")
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	if err := verifySignature(s.cfg.SigningSecret, sig, ts, body, s.now()); err != nil {
		slog.Warn("slack events: signature rejected", "err", err)
		chanlib.WriteErr(w, 401, "invalid signature")
		return
	}
	if s.bot == nil {
		chanlib.WriteErr(w, 503, "bot not ready")
		return
	}
	s.bot.handleEvent(body, w)
}

// handleFile proxies a cached file URL through the adapter so the agent
// fetches without seeing the bot token. Upstream gets `Authorization:
// Bearer xoxb-…` since Slack files require auth on url_private.
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
	req.Header.Set("Authorization", "Bearer "+s.cfg.BotToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		chanlib.WriteErr(w, 502, "cdn fetch failed")
		return
	}
	defer resp.Body.Close()
	chanlib.ProxyFile(w, resp, s.cfg.MediaMaxBytes)
}
