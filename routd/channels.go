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
// Register/list are gated on the adapter's ES256 service:<adapter> token
// (HMAC→ES256 retire step 4); deregister carries the adapter's own session
// token. With no Verifier (local dev) the gate falls back to CHANNEL_SECRET.
func (s *Server) mountChannels(mux *http.ServeMux) {
	if s.reg == nil {
		return
	}
	mux.HandleFunc("POST /v1/channels/register", s.channelAuth(s.handleChannelRegister))
	mux.HandleFunc("POST /v1/channels/deregister", s.handleChannelDeregister)
	mux.HandleFunc("GET /v1/channels", s.channelAuth(s.handleChannelList))
}

// channelAuth admits a caller presenting a valid ES256 service token (the
// adapter's service:<adapter>, verified via the Verifier's JWKS). The "service:"
// sub prefix is authd's marker for typ=service principals (authd never mints it
// for users), so the prefix check is the typ gate; a non-service sub or a verify
// failure is 401. With no Verifier (local dev, s.verify==nil) it falls back to
// the CHANNEL_SECRET constant-time compare.
//
// Trust note: the gate admits ANY seeded service principal, not one bound to
// req.Name. The registration `name` is the CHANNEL_NAME (telegram, telegram-rhias),
// while the token sub is the daemon principal (service:teled) — multi-account
// variants share one principal across many names, so name↔sub can't be pinned.
// The origin pin (handleChannelRegister) locks a name to its FIRST (originIP,
// principal) pair, blocking later hijack; first-claim squatting by a valid
// service principal is accepted, same as the prior CHANNEL_SECRET model (any
// secret holder could register any name) — ES256 only narrows the caller set.
func (s *Server) channelAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.verify == nil {
		return chanlib.Auth(s.reg.Secret(), next) // ks==nil → CHANNEL_SECRET compare
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
	// Origin pin: future re-registrations of this name must come from the same
	// source IP + same caller identity (blocks adapter-name hijack). Under ES256
	// the bearer is a service token that rotates ~hourly, so pin on the verified
	// service principal (stable per adapter), NOT the rotating token — pinning the
	// raw JWT would reject every legitimate re-register after a refresh. Local dev
	// (no Verifier) pins on the raw CHANNEL_SECRET, as before.
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
	if s.onRegister != nil {
		if entry := s.reg.Get(req.Name); entry != nil {
			s.onRegister(req.Name, chanreg.NewHTTPChannel(entry, s.reg.Bearer()))
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
