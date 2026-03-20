package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/channels/register", s.handleRegister)
	mux.HandleFunc("POST /v1/channels/deregister", s.handleDeregister)
	mux.HandleFunc("POST /v1/outbound", s.handleOutbound)
	mux.HandleFunc("POST /v1/messages", s.handleMessage)
	mux.HandleFunc("POST /v1/chats", s.handleChat)
	mux.HandleFunc("GET /v1/channels", s.handleListChannels)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

type registerReq struct {
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	JIDPrefixes  []string        `json:"jid_prefixes"`
	Capabilities map[string]bool `json:"capabilities"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.URL == "" || len(req.JIDPrefixes) == 0 {
		writeErr(w, http.StatusBadRequest, "name, url, jid_prefixes required")
		return
	}

	token, err := s.reg.Register(req.Name, req.URL, req.JIDPrefixes, req.Capabilities)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.onRegister != nil {
		entry := s.reg.Get(req.Name)
		s.onRegister(req.Name, chanreg.NewHTTPChannel(entry, s.reg.Secret()))
	}

	slog.Info("channel registered",
		"name", req.Name, "url", req.URL, "prefixes", req.JIDPrefixes)
	writeJSON(w, map[string]any{"ok": true, "token": token})
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
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}
	var req struct {
		JID  string `json:"jid"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JID == "" || req.Text == "" {
		writeErr(w, http.StatusBadRequest, "jid and text required")
		return
	}
	entry := s.reg.ForJID(req.JID)
	if entry == nil {
		writeErr(w, http.StatusNotFound, "no channel for jid")
		return
	}
	ch := chanreg.NewHTTPChannel(entry, s.reg.Secret())
	if err := ch.Send(req.JID, req.Text, ""); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

type messageReq struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chat_jid"`
	Sender      string `json:"sender"`
	SenderName  string `json:"sender_name"`
	Content     string `json:"content"`
	Timestamp   int64  `json:"timestamp"`
	IsGroup     bool   `json:"is_group"`
	ReplyTo     string `json:"reply_to"`
	Attachments []struct {
		Mime     string `json:"mime"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Size     int64  `json:"size"`
	} `json:"attachments"`
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	entry := s.checkToken(w, r)
	if entry == nil {
		return
	}

	var req messageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ChatJID == "" || req.Content == "" {
		writeErr(w, http.StatusBadRequest, "chat_jid and content required")
		return
	}

	if req.ID == "" {
		req.ID = fmt.Sprintf("%s-%d", entry.Name, time.Now().UnixNano())
	}

	ts := time.Now()
	if req.Timestamp > 0 {
		ts = time.Unix(req.Timestamp, 0)
	}

	msg := core.Message{
		ID:        req.ID,
		ChatJID:   req.ChatJID,
		Sender:    req.Sender,
		Name:      req.SenderName,
		Content:   req.Content,
		Timestamp: ts,
		ReplyToID: req.ReplyTo,
	}

	if err := s.store.PutMessage(msg); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		slog.Error("store message failed", "err", err)
		return
	}

	writeJSON(w, map[string]any{"ok": true})
}

type chatReq struct {
	ChatJID string `json:"chat_jid"`
	Name    string `json:"name"`
	IsGroup bool   `json:"is_group"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	entry := s.checkToken(w, r)
	if entry == nil {
		return
	}

	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ChatJID == "" {
		writeErr(w, http.StatusBadRequest, "chat_jid required")
		return
	}

	s.store.PutChat(req.ChatJID, req.Name, entry.Name, req.IsGroup)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

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
	writeJSON(w, map[string]any{"ok": true, "channels": out})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"status": "ok"})
}

func (s *Server) checkSecret(w http.ResponseWriter, r *http.Request) bool {
	if s := s.reg.Secret(); s != "" && extractBearer(r) != s {
		writeErr(w, http.StatusUnauthorized, "invalid secret")
		return false
	}
	return true
}

func (s *Server) checkToken(w http.ResponseWriter, r *http.Request) *chanreg.Entry {
	token := extractBearer(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return nil
	}
	if e := s.reg.ByToken(token); e != nil {
		return e
	}
	writeErr(w, http.StatusUnauthorized, "invalid token")
	return nil
}

func extractBearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
