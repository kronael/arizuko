package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
)

// envProfileKeyList in display order — fixed list from spec 5/42.
var envProfileKeyList = []string{
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"OPENAI_API_KEY",
	"CODEX_API_KEY",
}

// listUserEnvKeys returns the caller's env-profile keys that are currently set.
func (d *dash) listUserEnvKeys(sub string) ([]secretItem, error) {
	// Build an IN(...) list from the fixed key set.
	keys := make([]string, 0, len(store.EnvProfileKeys))
	args := []any{sub}
	placeholders := make([]string, 0, len(store.EnvProfileKeys))
	for k := range store.EnvProfileKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, k)
	}
	q := fmt.Sprintf(
		`SELECT key, created_at FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ? AND key IN (%s)
		 ORDER BY key`,
		strings.Join(placeholders, ","))
	rows, err := d.secretsDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []secretItem{}
	for rows.Next() {
		var it secretItem
		if err := rows.Scan(&it.Key, &it.CreatedAt); err != nil {
			slog.Warn("me_env scan", "err", err)
			continue
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// handleMeEnv serves GET /dash/me/env — HTML or JSON list of the caller's
// env-profile keys. Values are never returned.
func (d *dash) handleMeEnv(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.secretsDB() == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	items, err := d.listUserEnvKeys(sub)
	if err != nil {
		slog.Warn("me_env list", "sub", sub, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		d.renderEnvPage(w, r, items)
		return
	}
	type envItem struct {
		Key       string `json:"key"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]envItem, len(items))
	for i, it := range items {
		out[i] = envItem{Key: it.Key, CreatedAt: it.CreatedAt}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"env": out})
}

// renderEnvPage renders the model-API-key management page.
func (d *dash) renderEnvPage(w http.ResponseWriter, r *http.Request, items []secretItem) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Model API keys")
	fmt.Fprint(w, `<p class="dim">Your personal model API keys — injected into your agent container at spawn. They override the platform key. Once saved, the value is hidden; replace to update.</p>`)

	set := map[string]string{}
	for _, it := range items {
		set[it.Key] = it.CreatedAt
	}

	// Show all env-profile keys; those set by the user are editable rows,
	// those not set show the operator fallback note.
	var rows [][]string
	for _, k := range envProfileKeyList {
		if at, ok := set[k]; ok {
			del := fmt.Sprintf(
				`<button type="button" class="btn btn-danger" onclick="delEnvKey('%s')">delete</button>`,
				esc(k))
			rows = append(rows, []string{`<code>` + esc(k) + `</code>`, esc(at), del})
		} else {
			rows = append(rows, []string{`<code>` + esc(k) + `</code>`, `<span class="dim">platform key active</span>`, ""})
		}
	}
	fmt.Fprint(w, htmlTable([]string{"Key", "Updated", ""}, rows))

	fmt.Fprint(w, htmlSection("Set a key",
		`<form id="add-env" onsubmit="return addEnvKey(event)">`+
			htmlFormRow("Name", `<select name="key">`+envKeyOptions(set)+`</select>`)+
			htmlFormRow("Value", `<input type="password" name="value" autocomplete="off" required size="40">`)+
			`<p><button type="submit">save</button></p>`+
			`</form>`+
			`<p id="env-err" class="banner-err" style="display:none"></p>`))

	fmt.Fprint(w, envPageScript)
	pageClose(w, r)
}

func envKeyOptions(set map[string]string) string {
	var b strings.Builder
	for _, k := range envProfileKeyList {
		label := k
		if _, ok := set[k]; ok {
			label += " (replace)"
		}
		b.WriteString(`<option value="` + esc(k) + `">` + esc(label) + `</option>`)
	}
	return b.String()
}

const envPageScript = `<script>
async function addEnvKey(e){
  e.preventDefault();
  var f=e.target, err=document.getElementById('env-err');
  err.style.display='none';
  var res=await fetch('/dash/me/env',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({key:f.key.value,value:f.value.value})});
  if(res.ok){location.reload();}
  else{err.textContent=await res.text();err.style.display='block';}
  return false;
}
async function delEnvKey(k){
  if(!confirm('Delete '+k+'?'))return;
  var res=await fetch('/dash/me/env/'+encodeURIComponent(k),{method:'DELETE'});
  if(res.ok){location.reload();}
  else{var err=document.getElementById('env-err');err.textContent=await res.text();err.style.display='block';}
}
</script>`

// handleMeEnvCreate serves POST /dash/me/env — writes one env-profile key.
func (d *dash) handleMeEnvCreate(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := store.EnvProfileKeys[body.Key]; !ok {
		http.Error(w, body.Key+": not an env-profile key — use /dash/me/secrets for capability keys", http.StatusBadRequest)
		return
	}
	if err := ss.PutSecretRow(store.ScopeUser, sub, body.Key, body.Value); err != nil {
		slog.Warn("me_env create", "sub", sub, "key", body.Key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitEnvSet(sub, body.Key)
	slog.Info("me_env create", "sub", sub, "key", body.Key)
	w.WriteHeader(http.StatusNoContent)
}

// handleMeEnvUpdate serves PATCH /dash/me/env/{key} — replaces one env-profile key.
func (d *dash) handleMeEnvUpdate(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := store.EnvProfileKeys[key]; !ok {
		http.Error(w, key+": not an env-profile key", http.StatusBadRequest)
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
	var exists int
	switch err := db.QueryRow(
		`SELECT 1 FROM secrets WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		sub, key).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_env update exists", "sub", sub, "key", key, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := d.secretStore().PutSecretRow(store.ScopeUser, sub, key, body.Value); err != nil {
		slog.Warn("me_env update", "sub", sub, "key", key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitEnvSet(sub, key)
	slog.Info("me_env update", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}

// handleMeEnvDelete serves DELETE /dash/me/env/{key} — removes one env-profile key.
func (d *dash) handleMeEnvDelete(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := store.EnvProfileKeys[key]; !ok {
		http.Error(w, key+": not an env-profile key", http.StatusBadRequest)
		return
	}
	switch err := ss.DeleteSecretRow(store.ScopeUser, sub, key); {
	case errors.Is(err, store.ErrSecretNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_env delete", "sub", sub, "key", key, "err", err)
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
	slog.Info("me_env delete", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}

func emitEnvSet(sub, key string) {
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
