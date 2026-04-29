package admin

import (
	"encoding/json"
	"net/http"
)

// API exposes register/unregister/state/health endpoints used by consumers
// (e.g. arizuko gated) to populate the registry. Authentication is by
// network position: only reachable from the trusted internal network.
// No HMAC; bind address is internal-only.
type API struct {
	reg *Registry
}

func NewAPI(reg *Registry) *API {
	return &API{reg: reg}
}

type RegisterReq struct {
	IP        string   `json:"ip"`
	ID        string   `json:"id"`
	Allowlist []string `json:"allowlist"`
}

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
	type item struct {
		IP        string   `json:"ip"`
		ID        string   `json:"id"`
		Allowlist []string `json:"allowlist"`
	}
	out := make([]item, 0, len(snap))
	for ip, e := range snap {
		out = append(out, item{IP: ip, ID: e.ID, Allowlist: e.Allowlist})
	}
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
