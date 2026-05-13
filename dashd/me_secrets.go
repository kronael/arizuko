package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// keyPattern: uppercase ENV-style identifiers only.
var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

const maxSecretValueBytes = 8 << 10 // 8 KiB ceiling per secret value.

// requireUser returns the verified caller sub from X-User-Sub (set by
// proxyd's signed-identity middleware). Empty → 401, write a banner body.
func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	sub := strings.TrimSpace(r.Header.Get("X-User-Sub"))
	if sub == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return "", false
	}
	return sub, true
}

// requireSameOrigin is the CSRF guard for state-changing requests. The dash
// is fronted by proxyd; the user's browser submits forms/JS from the same
// origin. A missing Origin (curl, server-to-server) is allowed because the
// signed X-User-Sub already proves identity; a wrong Origin is rejected.
func requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	host := r.Host
	// Strip scheme from Origin to compare with Host header (host:port).
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	if origin != host {
		http.Error(w, "csrf: cross-origin write rejected", http.StatusForbidden)
		return false
	}
	return true
}

func (d *dash) handleMeSecrets(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.dbRW == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}

	rows, err := d.dbRW.Query(
		`SELECT key, created_at FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ?
		 ORDER BY key`, sub)
	if err != nil {
		slog.Warn("me_secrets list", "sub", sub, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type item struct {
		Key       string `json:"key"`
		CreatedAt string `json:"created_at"`
	}
	var items []item
	for rows.Next() {
		var key, createdAt string
		if err := rows.Scan(&key, &createdAt); err != nil {
			slog.Warn("me_secrets scan", "err", err)
			continue
		}
		items = append(items, item{Key: key, CreatedAt: createdAt})
	}
	w.Header().Set("Content-Type", "application/json")
	if items == nil {
		items = []item{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"secrets": items})
}

type secretWriteBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func parseSecretBody(r *http.Request) (secretWriteBody, error) {
	var body secretWriteBody
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxSecretValueBytes+1024)).Decode(&body); err != nil {
		return body, err
	}
	body.Key = strings.TrimSpace(body.Key)
	if body.Value == "" {
		return body, errors.New("value: empty value rejected")
	}
	if len(body.Value) > maxSecretValueBytes {
		return body, fmt.Errorf("value: exceeds %d bytes", maxSecretValueBytes)
	}
	return body, nil
}

func (d *dash) handleMeSecretCreate(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.dbRW == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := parseSecretBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !keyPattern.MatchString(body.Key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	if _, err := d.dbRW.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES ('user', ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   value = excluded.value, created_at = excluded.created_at`,
		sub, body.Key, body.Value, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		slog.Warn("me_secrets create", "sub", sub, "key", body.Key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	slog.Info("me_secrets create", "sub", sub, "key", body.Key)
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMeSecretUpdate(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.dbRW == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if !keyPattern.MatchString(key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	body, err := parseSecretBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Key != "" && body.Key != key {
		http.Error(w, "key: body key must match path", http.StatusBadRequest)
		return
	}
	res, err := d.dbRW.Exec(
		`UPDATE secrets SET value = ?, created_at = ?
		 WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		body.Value, time.Now().UTC().Format(time.RFC3339), sub, key)
	if err != nil {
		slog.Warn("me_secrets update", "sub", sub, "key", key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("me_secrets update", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMeSecretDelete(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.dbRW == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if !keyPattern.MatchString(key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	res, err := d.dbRW.Exec(
		`DELETE FROM secrets WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		sub, key)
	if err != nil {
		slog.Warn("me_secrets delete", "sub", sub, "key", key, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("me_secrets delete", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}
