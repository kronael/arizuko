package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/auth"
)

// serviceGrants maps a service principal to the scopes it may obtain at boot.
// Daemon-initiated work carries one of these (specs/5/1 "Service identity").
// Declared here, mechanical — no per-deployment DB state.
var serviceGrants = map[string][]string{
	"service:authd": {"keys:read"},
	"service:timed": {"messages:write", "tasks:read"},
	"service:onbod": {"messages:write", "groups:write"},
	"service:gated": {"messages:write", "messages:read", "tasks:read", "tasks:write"},
}

type server struct {
	a              *Authd
	serviceSecrets map[string]string // bootstrap key -> service principal (sub)
}

func (s *server) mux() *http.ServeMux {
	m := http.NewServeMux()
	// /v1/keys is public and mounts before any auth — backends fetch it to
	// verify offline.
	m.HandleFunc("GET /v1/keys", s.handleKeys)
	m.HandleFunc("POST /v1/service-token", s.handleServiceToken)
	m.HandleFunc("POST /v1/refresh", s.handleRefresh)
	m.HandleFunc("GET /health", s.handleHealth)
	return m
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if err := s.a.db.Ping(); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (s *server) handleKeys(w http.ResponseWriter, _ *http.Request) {
	body, err := auth.PublicJWKS(s.servingKeys()...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
}

func (s *server) servingKeys() []*auth.SigningKey {
	s.a.mu.RLock()
	defer s.a.mu.RUnlock()
	out := make([]*auth.SigningKey, 0, len(s.a.serving))
	for _, kr := range s.a.serving {
		out = append(out, kr.key)
	}
	return out
}

// handleServiceToken exchanges a bootstrap secret (AUTHD_SERVICE_KEY of one
// daemon) for a short access JWT whose sub is that daemon's service principal
// and whose scope is the principal's declared grants. The leaked secret buys
// only that one daemon's scoped token — never a signing capability.
func (s *server) handleServiceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	principal, ok := s.serviceSecrets[req.Key]
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	grants := serviceGrants[principal]
	token, err := s.a.MintForSubject(principal, nil, grants, "")
	if err != nil {
		slog.Error("mint service token", "principal", principal, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer"})
}

// handleRefresh rotates a refresh token, returning a new access + refresh pair.
// Reuse of a spent token revokes the family and returns 401.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	access, newRefresh, err := s.a.Refresh(req.RefreshToken)
	if err != nil {
		if err == errReuse {
			slog.Warn("refresh token reuse — family revoked")
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{
		"access_token":  access,
		"refresh_token": newRefresh,
		"token_type":    "Bearer",
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
