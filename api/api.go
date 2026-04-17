package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type Server struct {
	reg   *chanreg.Registry
	store *store.Store

	onRegister   func(name string, ch *chanreg.HTTPChannel)
	onDeregister func(name string)
}

func New(reg *chanreg.Registry, s *store.Store) *Server {
	return &Server{reg: reg, store: s}
}

func (s *Server) OnRegister(fn func(string, *chanreg.HTTPChannel)) { s.onRegister = fn }
func (s *Server) OnDeregister(fn func(string))                     { s.onDeregister = fn }

func (s *Server) Handler() http.Handler {
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return chanlib.Auth(s.reg.Secret(), h)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/channels/register", auth(s.handleRegister))
	mux.HandleFunc("POST /v1/channels/deregister", s.handleDeregister)
	mux.HandleFunc("POST /v1/outbound", auth(s.handleOutbound))
	mux.HandleFunc("POST /v1/messages", s.handleMessage)
	mux.HandleFunc("GET /v1/channels", auth(s.handleListChannels))
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleHealth)
	return mux
}

type registerReq struct {
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	JIDPrefixes  []string        `json:"jid_prefixes"`
	Capabilities map[string]bool `json:"capabilities"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		chanlib.WriteErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.URL == "" || len(req.JIDPrefixes) == 0 {
		chanlib.WriteErr(w, http.StatusBadRequest, "name, url, jid_prefixes required")
		return
	}

	token, err := s.reg.Register(req.Name, req.URL, req.JIDPrefixes, req.Capabilities)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.onRegister != nil {
		s.onRegister(req.Name, chanreg.NewHTTPChannel(s.reg.Get(req.Name), s.reg.Secret()))
	}

	slog.Info("channel registered",
		"name", req.Name, "url", req.URL, "prefixes", req.JIDPrefixes)
	chanlib.WriteJSON(w, map[string]any{"ok": true, "token": token})
}

func (s *Server) handleDeregister(w http.ResponseWriter, r *http.Request) {
	entry := s.checkToken(w, r)
	if entry == nil {
		return
	}

	s.reg.Deregister(entry.Name)
	if s.onDeregister != nil {
		s.onDeregister(entry.Name)
	}
	slog.Info("channel deregistered", "name", entry.Name)
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID     string `json:"jid"`
		Text    string `json:"text"`
		Channel string `json:"channel,omitempty"` // optional: explicit adapter override
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JID == "" || req.Text == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "jid and text required")
		return
	}
	// Resolution order: explicit channel → latest inbound source for jid → ForJID.
	name := req.Channel
	if name == "" {
		name = s.store.LatestSource(req.JID)
	}
	entry := s.reg.Resolve(name, req.JID)
	if entry == nil {
		chanlib.WriteErr(w, http.StatusNotFound, "no channel for jid")
		return
	}
	ch := chanreg.NewHTTPChannel(entry, s.reg.Secret())
	if _, err := ch.Send(req.JID, req.Text, "", ""); err != nil {
		chanlib.WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

type messageReq struct {
	ID            string `json:"id"`
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name"`
	Content       string `json:"content"`
	Timestamp     int64  `json:"timestamp"`
	ReplyTo       string `json:"reply_to"`
	ReplyToText   string `json:"reply_to_text"`
	ReplyToSender string `json:"reply_to_sender"`
	Topic         string `json:"topic,omitempty"`
	Verb          string `json:"verb,omitempty"`
	Attachments   []chanlib.InboundAttachment `json:"attachments"`
	// WhatsApp flat fields (whapd sends these instead of attachments array)
	Attachment     string `json:"attachment,omitempty"`      // base64
	AttachmentMime string `json:"attachment_mime,omitempty"`
	AttachmentName string `json:"attachment_name,omitempty"`
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	entry := s.checkToken(w, r)
	if entry == nil {
		return
	}

	var req messageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		chanlib.WriteErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ChatJID == "" || req.Content == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "chat_jid and content required")
		return
	}

	if !entry.Owns(req.ChatJID) {
		chanlib.WriteErr(w, http.StatusBadRequest, "jid prefix mismatch")
		return
	}

	if req.ID == "" {
		req.ID = core.MsgID(entry.Name)
	}

	ts := time.Now()
	if req.Timestamp > 0 {
		ts = time.Unix(req.Timestamp, 0)
	}

	if req.Attachment != "" {
		req.Attachments = append([]chanlib.InboundAttachment{{
			Mime:     req.AttachmentMime,
			Filename: req.AttachmentName,
			Data:     req.Attachment,
		}}, req.Attachments...)
	}
	var attsJSON string
	if len(req.Attachments) > 0 {
		if b, err := json.Marshal(req.Attachments); err == nil {
			attsJSON = string(b)
		}
	}
	verb := req.Verb
	if verb == "" {
		verb = "message"
	}
	msg := core.Message{
		ID:            req.ID,
		ChatJID:       req.ChatJID,
		Sender:        req.Sender,
		Name:          req.SenderName,
		Content:       req.Content,
		Timestamp:     ts,
		ReplyToID:     req.ReplyTo,
		ReplyToText:   req.ReplyToText,
		ReplyToSender: req.ReplyToSender,
		Topic:         req.Topic,
		Verb:          verb,
		Attachments:   attsJSON,
		Source:        entry.Name,
	}

	if err := s.store.PutMessage(msg); err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, "store failed")
		slog.Error("store message failed", "err", err)
		return
	}

	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	entries := s.reg.All()
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"name":         e.Name,
			"url":          e.URL,
			"jid_prefixes": e.JIDPrefixes,
			"capabilities": e.Capabilities,
			"health_fails": e.HealthFails,
			"registered_at": e.RegisteredAt.Unix(),
		})
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true, "channels": out})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"status": "ok"})
}

func (s *Server) checkToken(w http.ResponseWriter, r *http.Request) *chanreg.Entry {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if e := s.reg.ByToken(token); e != nil {
		return e
	}
	chanlib.WriteErr(w, http.StatusUnauthorized, "invalid token")
	return nil
}
