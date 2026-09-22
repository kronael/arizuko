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

// The channel-registration surface: adapters register their egress URL +
// owned jid prefixes, and routd's Deliverer resolves them on the way out. The
// registry is in-memory, so adapters re-register on routd restart (chanlib
// retries on 401).

// SetChannelRegistry wires the registry; call it before Handler(). nil leaves
// the channel endpoints unmounted.
func (s *Server) SetChannelRegistry(reg *chanreg.Registry) {
	s.reg = reg
}

// mountChannels adds the channel-registration routes. Register/list are gated
// on the adapter's ES256 service:<adapter> token (spec 5/1); deregister
// carries the adapter's own session token.
func (s *Server) mountChannels(mux *http.ServeMux) {
	if s.reg == nil {
		return
	}
	mux.HandleFunc("POST /v1/channels/register", s.channelAuth(s.handleChannelRegister))
	mux.HandleFunc("POST /v1/channels/deregister", s.handleChannelDeregister)
	mux.HandleFunc("GET /v1/channels", s.channelAuth(s.handleChannelList))
}

// channelAuth admits a caller presenting a valid ES256 service token; the
// "service:" sub prefix is authd's marker for typ=service, so it is the typ
// gate. It admits ANY seeded service principal, not one bound to req.Name:
// the registration `name` is the CHANNEL_NAME while the token sub is the
// daemon principal, and multi-account variants share one. The origin pin
// locks a name to its FIRST (originIP, principal) pair; first-claim squatting
// by a seeded principal is accepted.
func (s *Server) channelAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.verify == nil {
		return next // local dev: no JWKS to verify against
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sub, _, _, err := s.verify.Verify(r)
		if err != nil || !strings.HasPrefix(sub, "service:") {
			chanlib.WriteErr(w, http.StatusUnauthorized, "invalid service token")
			return
		}
		next(w, r)
	}
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
	// Origin pin: re-registrations of this name must come from the same source
	// IP + caller identity. Under ES256 the bearer rotates ~hourly, so pin on
	// the verified service principal, NOT the token, or every legitimate
	// re-register after a refresh is rejected. Local dev has nothing to pin.
	originIP := clientIP(r)
	pinID := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.verify != nil {
		if sub, _, _, err := s.verify.Verify(r); err == nil {
			pinID = sub
		}
	}
	token, err := s.reg.RegisterWithOrigin(req.Name, req.URL, req.JIDPrefixes, req.Capabilities,
		originIP, pinID)
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
