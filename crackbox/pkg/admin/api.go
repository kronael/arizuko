package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

// API exposes register/unregister/state/health endpoints used by consumers
// (e.g. arizuko gated) to populate the registry. Mutating endpoints
// (register/unregister) optionally require a bearer token; read-only
// endpoints (state/health) are always open.
type API struct {
	reg       *Registry
	proxyAddr string // host:port for the /health self-test
	secret    string // optional bearer token; empty disables auth
}

func NewAPI(reg *Registry) *API {
	return &API{reg: reg, proxyAddr: "127.0.0.1:3128"}
}

// NewAPIWithProxy threads the proxy listen address through so /health
// can verify the proxy port is also accepting connections. If addr is a
// bare ":3128"-style listen spec, the host is rewritten to 127.0.0.1.
func NewAPIWithProxy(reg *Registry, proxyAddr string) *API {
	if strings.HasPrefix(proxyAddr, ":") {
		proxyAddr = "127.0.0.1" + proxyAddr
	}
	if proxyAddr == "" {
		proxyAddr = "127.0.0.1:3128"
	}
	return &API{reg: reg, proxyAddr: proxyAddr}
}

// WithSecret enables bearer-token auth on mutating endpoints. Empty
// secret leaves auth disabled (backwards-compatible).
func (a *API) WithSecret(secret string) *API {
	a.secret = secret
	return a
}

// authOK returns true if no secret is configured or if the request
// carries a matching Authorization: Bearer <secret> header. Constant-time
// compare avoids timing leaks.
func (a *API) authOK(r *http.Request) bool {
	if a.secret == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := h[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.secret)) == 1
}

// WireEntry is the shared wire shape for register requests and state
// responses. Single definition prevents drift between API + client.
type WireEntry struct {
	IP        string   `json:"ip"`
	ID        string   `json:"id"`
	Allowlist []string `json:"allowlist"`
}

// RegisterReq is the body of POST /v1/register. Alias kept for clarity at
// call sites; identical wire shape to WireEntry.
type RegisterReq = WireEntry

type UnregisterReq struct {
	IP string `json:"ip"`
}

func (a *API) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/v1/register", a.register)
	m.HandleFunc("/v1/unregister", a.unregister)
	m.HandleFunc("/v1/state", a.state)
	m.HandleFunc("/health", a.health)
	return m
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !a.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req RegisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.IP == "" {
		http.Error(w, "ip required", http.StatusBadRequest)
		return
	}
	a.reg.Set(req.IP, req.ID, req.Allowlist)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) unregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !a.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req UnregisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.reg.Remove(req.IP)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) state(w http.ResponseWriter, r *http.Request) {
	snap := a.reg.Snapshot()
	out := make([]WireEntry, 0, len(snap))
	for ip, e := range snap {
		out = append(out, WireEntry{IP: ip, ID: e.ID, Allowlist: e.Allowlist})
	}
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	// Self-test the proxy listener: a green admin page when the proxy is
	// dead is worse than no healthcheck at all (silent failure).
	conn, err := net.DialTimeout("tcp", a.proxyAddr, 1*time.Second)
	if err != nil {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"proxy_down"}`))
		return
	}
	conn.Close()
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
