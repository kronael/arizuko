package main

import (
	"encoding/json"
	"net/http"
)

// API exposes register/unregister/list endpoints used by gated when an agent
// container spawns or exits. Authentication is by network position: only
// reachable from the internal Docker network where gated and egred share a
// trust boundary. No HMAC here — bind address is internal-only.

type API struct {
	allow *Allowlist
}

func NewAPI(allow *Allowlist) *API {
	return &API{allow: allow}
}

type registerReq struct {
	IP        string   `json:"ip"`
	Folder    string   `json:"folder"`
	Allowlist []string `json:"allowlist"`
}

type unregisterReq struct {
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
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.IP == "" {
		http.Error(w, "ip required", http.StatusBadRequest)
		return
	}
	a.allow.Set(req.IP, req.Folder, req.Allowlist)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) unregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req unregisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.allow.Remove(req.IP)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) state(w http.ResponseWriter, r *http.Request) {
	snap := a.allow.Snapshot()
	type item struct {
		IP        string   `json:"ip"`
		Folder    string   `json:"folder"`
		Allowlist []string `json:"allowlist"`
	}
	out := make([]item, 0, len(snap))
	for ip, e := range snap {
		out = append(out, item{IP: ip, Folder: e.folder, Allowlist: e.allowlist})
	}
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
