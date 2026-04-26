package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

// Request body caps. /v1/messages is larger to accommodate inline base64
// attachments (whapd); other endpoints take small JSON.
const (
	maxBodyDefault  = 10 << 20 // 10 MiB
	maxBodyMessages = 25 << 20 // 25 MiB
)

type Server struct {
	reg   *chanreg.Registry
	store *store.Store

	onRegister    func(name string, ch *chanreg.HTTPChannel)
	onDeregister  func(name string)
	channelLookup func(name string) *chanreg.HTTPChannel
}

func New(reg *chanreg.Registry, s *store.Store) *Server {
	return &Server{reg: reg, store: s}
}

func (s *Server) OnRegister(fn func(string, *chanreg.HTTPChannel)) { s.onRegister = fn }
func (s *Server) OnDeregister(fn func(string))                     { s.onDeregister = fn }

// ChannelLookup wires the server to the gateway's live channel store so
// outbound sends reuse the same HTTPChannel (preserves retry outbox).
func (s *Server) ChannelLookup(fn func(name string) *chanreg.HTTPChannel) {
	s.channelLookup = fn
}

func (s *Server) Handler() http.Handler {
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return chanlib.Auth(s.reg.Secret(), h)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/channels/register", auth(capBody(maxBodyDefault, s.handleRegister)))
	mux.HandleFunc("POST /v1/channels/deregister", capBody(maxBodyDefault, s.handleDeregister))
	mux.HandleFunc("POST /v1/outbound", auth(capBody(maxBodyDefault, s.handleOutbound)))
	mux.HandleFunc("POST /v1/messages", capBody(maxBodyMessages, s.handleMessage))
	mux.HandleFunc("GET /v1/channels", auth(s.handleListChannels))
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleHealth)
	return mux
}

// capBody wraps next with an http.MaxBytesReader on r.Body so an
// oversize request body surfaces a 400-class error before it exhausts
// memory in json.Decode.
func capBody(max int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, max)
		next(w, r)
	}
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

	// Origin pin: require future re-registrations to come from the same
	// source IP and present the same secret. Blocks adapter-name hijack
	// by other CHANNEL_SECRET holders.
	originIP := clientIP(r)
	presentedSecret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	token, err := s.reg.RegisterWithOrigin(req.Name, req.URL, req.JIDPrefixes, req.Capabilities,
		originIP, presentedSecret)
	if err != nil {
		status := http.StatusBadRequest
		if strings.HasPrefix(err.Error(), "generate token") {
			status = http.StatusInternalServerError
		} else if strings.Contains(err.Error(), "already registered") {
			status = http.StatusConflict
		}
		chanlib.WriteErr(w, status, err.Error())
		return
	}

	if s.onRegister != nil {
		if entry := s.reg.Get(req.Name); entry != nil {
			s.onRegister(req.Name, chanreg.NewHTTPChannel(entry, s.reg.Secret()))
		}
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
	// Prefer the gateway-held HTTPChannel (set via OnRegister) so its
	// outbox survives across failed sends. Fall back to constructing one
	// when no gateway hook is wired (tests, bootstrap).
	ch := s.resolveChannel(entry)
	if _, err := ch.SendCtx(r.Context(), req.JID, req.Text, "", ""); err != nil {
		chanlib.WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *Server) resolveChannel(entry *chanreg.Entry) *chanreg.HTTPChannel {
	if s.channelLookup != nil {
		if ch := s.channelLookup(entry.Name); ch != nil {
			return ch
		}
	}
	return chanreg.NewHTTPChannel(entry, s.reg.Secret())
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
	Reaction      string `json:"reaction,omitempty"`
	Attachments   []chanlib.InboundAttachment `json:"attachments"`
	IsGroup       bool                        `json:"is_group,omitempty"`
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

	if err := s.store.SetChatIsGroup(req.ChatJID, req.IsGroup); err != nil {
		slog.Warn("set chat is_group failed", "jid", req.ChatJID, "err", err)
	}

	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

type channelDTO struct {
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	JIDPrefixes  []string        `json:"jid_prefixes"`
	Capabilities map[string]bool `json:"capabilities"`
	HealthFails  int             `json:"health_fails"`
	RegisteredAt int64           `json:"registered_at"`
}

type listChannelsResp struct {
	OK       bool         `json:"ok"`
	Channels []channelDTO `json:"channels"`
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	entries := s.reg.All()
	out := make([]channelDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, channelDTO{
			Name:         e.Name,
			URL:          e.URL,
			JIDPrefixes:  e.JIDPrefixes,
			Capabilities: e.Capabilities,
			HealthFails:  e.HealthFails,
			RegisteredAt: e.RegisteredAt.Unix(),
		})
	}
	chanlib.WriteJSON(w, listChannelsResp{OK: true, Channels: out})
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

// clientIP returns the peer IP for r. RemoteAddr is host:port; strip the port.
// No X-Forwarded-For trust: router is private-network-bound.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
