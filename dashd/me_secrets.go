package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
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

type secretItem struct {
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}

// listUserSecrets returns the caller's secret KEYS + created_at — never values
// (the list surface mirrors GitHub SSH keys: names visible, secrets opaque).
func (d *dash) listUserSecrets(sub string) ([]secretItem, error) {
	rows, err := d.secretsDB().Query(
		`SELECT key, created_at FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ?
		 ORDER BY key`, sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []secretItem{}
	for rows.Next() {
		var it secretItem
		if err := rows.Scan(&it.Key, &it.CreatedAt); err != nil {
			slog.Warn("me_secrets scan", "err", err)
			continue
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// handleMeSecrets serves the user's secret list: an HTML management page for a
// browser (Accept: text/html) and JSON for API callers. Values are never
// returned on either surface.
func (d *dash) handleMeSecrets(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.secretsDB() == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	items, err := d.listUserSecrets(sub)
	if err != nil {
		slog.Warn("me_secrets list", "sub", sub, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		d.renderSecretsPage(w, r, items)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"secrets": items})
}

// renderSecretsPage renders the BYOA management page: a key/created table with a
// per-row delete button + an add form. No values displayed. The delete button
// fires DELETE /dash/me/secrets/{key} via fetch (same-origin → passes the CSRF
// guard) and reloads. esc() guards every interpolated key.
func (d *dash) renderSecretsPage(w http.ResponseWriter, r *http.Request, items []secretItem) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "API keys")
	fmt.Fprint(w, `<p class="dim">Your personal API keys. They override the group's keys when the agent runs for you. Once saved, a key's value is hidden — you can replace it but never read it back.</p>`)

	if len(items) == 0 {
		fmt.Fprint(w, `<p class="empty">No API keys yet.</p>`)
	} else {
		var rows [][]string
		for _, it := range items {
			del := fmt.Sprintf(
				`<button type="button" class="btn btn-danger" onclick="delSecret('%s')">delete</button>`,
				esc(it.Key))
			rows = append(rows, []string{`<code>` + esc(it.Key) + `</code>`, esc(it.CreatedAt), del})
		}
		// "Updated" not "Added": created_at is reset on every upsert (PutSecretRow
		// ON CONFLICT), so it reflects the last write, not first-create.
		fmt.Fprint(w, htmlTable([]string{"Key", "Updated", ""}, rows))
	}

	fmt.Fprint(w, htmlSection("Add a key",
		`<form id="add-secret" onsubmit="return addSecret(event)">`+
			htmlFormRow("Name", `<input type="text" name="key" placeholder="GITHUB_TOKEN" pattern="[A-Z][A-Z0-9_]*" title="uppercase letters, digits, underscore" required size="40">`)+
			htmlFormRow("Value", `<input type="password" name="value" autocomplete="off" required size="40">`)+
			`<p><button type="submit">save</button></p>`+
			`</form>`+
			`<p id="secret-err" class="banner-err" style="display:none"></p>`))

	fmt.Fprint(w, secretsPageScript)
	pageClose(w, r)
}

// secretsPageScript drives the add/delete forms with fetch (same-origin → CSRF
// guard passes). Keys are URL-encoded into the path; the server re-validates the
// key pattern and the value, so the client checks are UX only.
const secretsPageScript = `<script>
async function addSecret(e){
  e.preventDefault();
  var f=e.target, err=document.getElementById('secret-err');
  err.style.display='none';
  var res=await fetch('/dash/me/secrets',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({key:f.key.value,value:f.value.value})});
  if(res.ok){location.reload();}
  else{err.textContent=await res.text();err.style.display='block';}
  return false;
}
async function delSecret(k){
  if(!confirm('Delete '+k+'?'))return;
  var res=await fetch('/dash/me/secrets/'+encodeURIComponent(k),{method:'DELETE'});
  if(res.ok){location.reload();}
  else{var err=document.getElementById('secret-err');err.textContent=await res.text();err.style.display='block';}
}
</script>`

type secretWriteBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func parseSecretBody(w http.ResponseWriter, r *http.Request) (secretWriteBody, error) {
	var body secretWriteBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSecretValueBytes+1024)).Decode(&body); err != nil {
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
	ss := d.secretStore()
	if ss == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := parseSecretBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !keyPattern.MatchString(body.Key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	if _, ok := store.EnvProfileKeys[body.Key]; ok {
		http.Error(w, body.Key+": model API key — use /dash/me/env", http.StatusBadRequest)
		return
	}
	// Seal at rest via secretStore (SECRETS_KEY keyring): PutSecretRow upserts the
	// `v2:` ciphertext — the same encoding routd reads back through FolderSecrets.
	if err := ss.PutSecretRow(store.ScopeUser, sub, body.Key, body.Value); err != nil {
		slog.Warn("me_secrets create", "sub", sub, "key", body.Key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitSecretSet(sub, body.Key)
	slog.Info("me_secrets create", "sub", sub, "key", body.Key)
	w.WriteHeader(http.StatusNoContent)
}

// emitSecretSet records a user-secret write in audit_log WITHOUT the plaintext
// value (audit_log is at rest; the value must never land there).
func emitSecretSet(sub, key string) {
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.set",
		Actor:    "user:" + sub,
		ActorSub: sub,
		Surface:  audit.SurfaceREST,
		Resource: "secrets/user/" + sub + "/" + key,
		Scope:    "user",
		Outcome:  audit.OutcomeOK,
	})
}

func (d *dash) handleMeSecretUpdate(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	db := d.secretsDB()
	if db == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if !keyPattern.MatchString(key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	if _, ok := store.EnvProfileKeys[key]; ok {
		http.Error(w, key+": model API key — use /dash/me/env", http.StatusBadRequest)
		return
	}
	body, err := parseSecretBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Key != "" && body.Key != key {
		http.Error(w, "key: body key must match path", http.StatusBadRequest)
		return
	}
	// PATCH is update-only: 404 when the key was never set (the create path is
	// POST). Existence is checked on the raw handle; the reseal goes through
	// secretStore so the new value lands as a `v2:` ciphertext.
	var exists int
	switch err := db.QueryRow(
		`SELECT 1 FROM secrets WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		sub, key).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_secrets update exists", "sub", sub, "key", key, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := d.secretStore().PutSecretRow(store.ScopeUser, sub, key, body.Value); err != nil {
		slog.Warn("me_secrets update", "sub", sub, "key", key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitSecretSet(sub, key)
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
	ss := d.secretStore()
	if ss == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if !keyPattern.MatchString(key) {
		http.Error(w, "key: must match ^[A-Z][A-Z0-9_]*$", http.StatusBadRequest)
		return
	}
	if _, ok := store.EnvProfileKeys[key]; ok {
		http.Error(w, key+": model API key — use /dash/me/env", http.StatusBadRequest)
		return
	}
	// One writer: DeleteSecretRow owns the WHERE clause + the 0-rows → 404 case
	// (ErrSecretNotFound). No keyring needed to delete, but routing through the
	// store keeps the delete SQL in one place with create/update.
	switch err := ss.DeleteSecretRow(store.ScopeUser, sub, key); {
	case errors.Is(err, store.ErrSecretNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_secrets delete", "sub", sub, "key", key, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.delete",
		Actor:    "user:" + sub,
		ActorSub: sub,
		Surface:  audit.SurfaceREST,
		Resource: "secrets/user/" + sub + "/" + key,
		Scope:    "user",
		Outcome:  audit.OutcomeOK,
	})
	slog.Info("me_secrets delete", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}
