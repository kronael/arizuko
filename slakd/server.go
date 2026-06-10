package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

const maxEventBody = 1 << 20

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
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"slack:"}, s.bot, s.isConnected, s.lastInboundAt)
	// Events webhook is signature-verified, not chanlib.Auth-gated; file proxy adds Bearer xoxb upstream.
	mux.HandleFunc("POST /slack/events", s.handleEvents)
	mux.HandleFunc("GET /files/", chanlib.Auth(chanlib.FileProxyHandler(chanlib.FileProxyOpts{
		Resolve: s.files.Get,
		Decorate: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+s.cfg.BotToken)
		},
		MaxBytes: s.cfg.MediaMaxBytes,
	})))
	mux.HandleFunc("POST /v1/pane/prompts", chanlib.Auth(s.handlePanePrompts))
	mux.HandleFunc("POST /v1/pane/title", chanlib.Auth(s.handlePaneTitle))
	return mux
}

// pane control endpoints — gated POSTs here when the agent calls
// pane_set_prompts / pane_set_title via MCP. Both stash values into
// the bot's pending slot; values fire after the next chat.postMessage
// into the pane (one outbound = one drained set).
type panePromptsReq struct {
	JID     string       `json:"jid"`
	Prompts []panePrompt `json:"prompts"`
}

func (s *server) handlePanePrompts(w http.ResponseWriter, r *http.Request) {
	var req panePromptsReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		chanlib.WriteErr(w, 400, "invalid json")
		return
	}
	if err := s.bot.stagePanePromptsByJID(req.JID, req.Prompts); err != nil {
		chanlib.WriteErr(w, 404, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

type paneTitleReq struct {
	JID   string `json:"jid"`
	Title string `json:"title"`
}

func (s *server) handlePaneTitle(w http.ResponseWriter, r *http.Request) {
	var req paneTitleReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		chanlib.WriteErr(w, 400, "invalid json")
		return
	}
	if err := s.bot.stagePaneTitleByJID(req.JID, req.Title); err != nil {
		chanlib.WriteErr(w, 404, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// handleEvents requires verbatim body bytes — any upstream re-marshal breaks the HMAC.
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
