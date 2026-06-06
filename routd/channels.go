package routd

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
)

// The channel-registration surface. Adapters register their egress URL + owned
// jid prefixes here (POST /v1/channels/register); routd's Deliverer resolves
// them on the way out. The registry is in-memory: adapters re-register on routd
// restart (chanlib retries on 401).

// SetChannelRegistry wires the channel registry + the live-channel hooks so the
// Deliverer reuses the per-adapter HTTPChannel (preserving its retry outbox)
// instead of constructing a throwaway per send. Call before Handler(). reg==nil
// leaves the channel endpoints unmounted (pure REST tests / no adapters).
func (s *Server) SetChannelRegistry(reg *chanreg.Registry,
	onRegister func(name string, ch *chanreg.HTTPChannel), onDeregister func(name string)) {
	s.reg = reg
	s.onRegister = onRegister
	s.onDeregister = onDeregister
}

// mountChannels adds the channel-registration routes when a registry is wired.
// Register/list are CHANNEL_SECRET-gated (chanlib.Auth); deregister carries the
// adapter's own session token.
func (s *Server) mountChannels(mux *http.ServeMux) {
	if s.reg == nil {
		return
	}
	auth := func(h http.HandlerFunc) http.HandlerFunc { return chanlib.Auth(s.reg.Secret(), h) }
	mux.HandleFunc("POST /v1/channels/register", auth(s.handleChannelRegister))
	mux.HandleFunc("POST /v1/channels/deregister", s.handleChannelDeregister)
	mux.HandleFunc("GET /v1/channels", auth(s.handleChannelList))
}

type channelRegisterReq struct {
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	JIDPrefixes  []string        `json:"jid_prefixes"`
	Capabilities map[string]bool `json:"capabilities"`
}

func (s *Server) handleChannelRegister(w http.ResponseWriter, r *http.Request) {
	var req channelRegisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		chanlib.WriteErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.URL == "" || len(req.JIDPrefixes) == 0 {
		chanlib.WriteErr(w, http.StatusBadRequest, "name, url, jid_prefixes required")
		return
	}
	// Origin pin: future re-registrations of this name must come from the same
	// source IP + present the same secret (blocks adapter-name hijack by other
	// CHANNEL_SECRET holders).
	originIP := clientIP(r)
	presentedSecret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	token, err := s.reg.RegisterWithOrigin(req.Name, req.URL, req.JIDPrefixes, req.Capabilities,
		originIP, presentedSecret)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case strings.HasPrefix(err.Error(), "generate token"):
			status = http.StatusInternalServerError
		case strings.Contains(err.Error(), "already registered"):
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
	slog.Info("channel registered", "name", req.Name, "url", req.URL, "prefixes", req.JIDPrefixes)
	chanlib.WriteJSON(w, map[string]any{"ok": true, "token": token})
}

func (s *Server) handleChannelDeregister(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	entry := s.reg.ByToken(token)
	if entry == nil {
		chanlib.WriteErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	s.reg.Deregister(entry.Name)
	if s.onDeregister != nil {
		s.onDeregister(entry.Name)
	}
	slog.Info("channel deregistered", "name", entry.Name)
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

func (s *Server) handleChannelList(w http.ResponseWriter, _ *http.Request) {
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
	chanlib.WriteJSON(w, map[string]any{"ok": true, "channels": out})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
