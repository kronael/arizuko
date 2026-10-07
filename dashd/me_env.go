package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/dashd/dashstore"
	"github.com/kronael/arizuko/store"
)

// notEnvProfileKeySuffix is appended to every rejection across all three
// verbs (create/update/delete) so a fourth cannot invent a fifth wording
// (BUGS F22: POST already carried the pointer, PATCH/DELETE truncated it).
const notEnvProfileKeySuffix = ": not an env-profile key — use /dash/me/secrets for capability keys"

// envProfileKeyList is the page's display order, DERIVED from
// store.EnvProfileKeys rather than restated. It was a second literal of the
// same facts, and when `9e55216e` added ANTHROPIC_OAUTH_TOKEN to the map the
// page silently stopped offering it — so a user on the pi backend had no
// operator surface for the one credential that backend reads (BUGS L91).
// Sorted, because a map has no order and a stable page needs one.
var envProfileKeyList = sortedEnvProfileKeys()

func sortedEnvProfileKeys() []string {
	out := make([]string, 0, len(store.EnvProfileKeys))
	for k := range store.EnvProfileKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// listUserEnvKeys returns the caller's env-profile keys that are currently set.
// The fixed display-order key list bounds the answer, so a secret outside the
// profile cannot reach this page.
func (d *dash) listUserEnvKeys(sub string) ([]secretItem, error) {
	return d.ds().UserEnvKeys(sub, envProfileKeyList)
}

// handleMeEnv serves GET /dash/me/env — HTML or JSON list of the caller's
// env-profile keys. Values are never returned.
func (d *dash) handleMeEnv(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.ds() == nil {
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
	pageTopFor(w, r, "Model API keys")
	_, _ = fmt.Fprint(w, `<p class="dim">Your personal model API keys — injected into your agent container at spawn. They override the platform key. Once saved, the value is hidden; replace to update.</p>`)

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
	_, _ = fmt.Fprint(w, htmlTable([]string{"Key", "Updated", ""}, rows))

	_, _ = fmt.Fprint(w, htmlSection("Set a key",
		`<form id="add-env" onsubmit="return addEnvKey(event)">`+
			htmlFormRow("Name", `<select name="key">`+envKeyOptions(set)+`</select>`)+
			htmlFormRow("Value", `<input type="password" name="value" autocomplete="off" required size="40">`)+
			`<p><button type="submit">save</button></p>`+
			`</form>`+
			`<p id="env-err" class="banner-err" style="display:none"></p>`))

	_, _ = fmt.Fprint(w, envPageScript)
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
	sub, ss, ok := d.userSecretWrite(w, r)
	if !ok {
		return
	}
	body, err := parseSecretBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := store.EnvProfileKeys[body.Key]; !ok {
		http.Error(w, body.Key+notEnvProfileKeySuffix, http.StatusBadRequest)
		return
	}
	if err := ss.PutSecretRow(store.ScopeUser, sub, body.Key, body.Value); err != nil {
		slog.Warn("me_env create", "sub", sub, "key", body.Key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitSecretSet(sub, body.Key)
	slog.Info("me_env create", "sub", sub, "key", body.Key)
	w.WriteHeader(http.StatusNoContent)
}

// handleMeEnvUpdate serves PATCH /dash/me/env/{key} — replaces one env-profile key.
func (d *dash) handleMeEnvUpdate(w http.ResponseWriter, r *http.Request) {
	sub, ss, ok := d.userSecretWrite(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if _, ok := store.EnvProfileKeys[key]; !ok {
		http.Error(w, key+notEnvProfileKeySuffix, http.StatusBadRequest)
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
	switch err := d.ds().UserSecretExists(sub, key); {
	case errors.Is(err, dashstore.ErrSecretMissing):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_env update exists", "sub", sub, "key", key, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := ss.PutSecretRow(store.ScopeUser, sub, key, body.Value); err != nil {
		slog.Warn("me_env update", "sub", sub, "key", key, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitSecretSet(sub, key)
	slog.Info("me_env update", "sub", sub, "key", key)
	w.WriteHeader(http.StatusNoContent)
}

// handleMeEnvDelete serves DELETE /dash/me/env/{key} — removes one env-profile key.
func (d *dash) handleMeEnvDelete(w http.ResponseWriter, r *http.Request) {
	sub, ss, ok := d.userSecretWrite(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if _, ok := store.EnvProfileKeys[key]; !ok {
		http.Error(w, key+notEnvProfileKeySuffix, http.StatusBadRequest)
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

// secret.set audit for /dash/me/env writes is emitted via emitSecretSet
// (dashd/me_secrets.go) — same user-scoped secrets/user/<sub>/<key> shape.
