package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// me_connections.go serves the surrogate-OAuth "Connect <provider>" dance
// (spec 5/15): arizuko authenticates AS the signed-in user to a provider API and
// writes the access+refresh token into the SAME user-scoped secrets row a pasted
// PAT lands in — only the writer differs. Reuses auth's PKCE + CSRF-state
// primitives; the engine (d.surrogate) drives the exchange/refresh.

// cookieSecure reports whether the dance's cookies should carry Secure. dashd
// sits behind proxyd (TLS-terminating), so trust X-Forwarded-Proto too.
func cookieSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// callbackURL is the absolute redirect_uri for a provider's callback. It must
// match the provider app's registered callback, so it is built from the
// configured connBaseURL, never from the request host.
func (d *dash) callbackURL(provider string) string {
	return d.connBaseURL + "/dash/me/connections/" + provider + "/callback"
}

type connectionItem struct {
	Provider  string `json:"provider"`
	Key       string `json:"key"`
	Scope     string `json:"scope,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Connected bool   `json:"connected"`
}

// listConnections returns one item per registered provider, marking those the
// caller has connected (a user-scoped OAuth row with that provider).
func (d *dash) listConnections(sub string) ([]connectionItem, error) {
	ss := d.secretStore()
	if ss == nil || d.surrogate == nil {
		return nil, errors.New("connections unavailable")
	}
	conns, err := ss.ListUserConnections(sub)
	if err != nil {
		return nil, err
	}
	byProvider := make(map[string]store.OAuthSecret, len(conns))
	for _, c := range conns {
		byProvider[c.Provider] = c
	}
	var out []connectionItem
	for _, name := range d.surrogate.Names() {
		it := connectionItem{Provider: name}
		if c, ok := byProvider[name]; ok {
			it.Connected = true
			it.Key = c.Key
			it.Scope = c.Scope
			if !c.ExpiresAt.IsZero() {
				it.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// handleMeConnections lists the caller's OAuth connections — HTML for a browser,
// JSON for API callers. Never returns token values.
func (d *dash) handleMeConnections(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.surrogate == nil || d.secretStore() == nil {
		http.Error(w, "connections unavailable", http.StatusServiceUnavailable)
		return
	}
	items, err := d.listConnections(sub)
	if err != nil {
		slog.Warn("me_connections list", "sub", sub, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		d.renderConnectionsPage(w, r, items)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connections": items})
}

func (d *dash) renderConnectionsPage(w http.ResponseWriter, r *http.Request, items []connectionItem) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "OAuth connections")
	fmt.Fprint(w, `<p class="dim">Connect a provider to let the agent act as you against its API. A connection writes a scoped, auto-refreshing token — the OAuth alternative to pasting a personal access token under <a href="/dash/me/secrets">API keys</a>.</p>`)

	var rows [][]string
	for _, it := range items {
		var status, action string
		if it.Connected {
			status = `<span class="dot dot-ok"></span>connected`
			if it.ExpiresAt != "" {
				status += ` <span class="dim">(expires <abbr title="` + esc(it.ExpiresAt) + `">` + relativeTS(it.ExpiresAt) + `</abbr>)</span>`
			}
			action = fmt.Sprintf(`<button type="button" class="btn btn-danger" onclick="disconnect('%s')">disconnect</button>`, esc(it.Provider))
		} else {
			status = `<span class="dot dot-unknown"></span>not connected`
			action = fmt.Sprintf(`<form method="post" action="/dash/me/connections/%s/start" style="display:inline"><button type="submit">connect</button></form>`, esc(it.Provider))
		}
		scope := it.Scope
		if scope == "" {
			scope = "—"
		}
		rows = append(rows, []string{`<code>` + esc(it.Provider) + `</code>`, status, esc(scope), action})
	}
	if len(rows) == 0 {
		fmt.Fprint(w, `<p class="empty">No providers configured.</p>`)
	} else {
		fmt.Fprint(w, htmlTable([]string{"Provider", "Status", "Scopes", ""}, rows))
	}
	fmt.Fprint(w, `<p id="conn-err" class="banner-err" style="display:none"></p>`)
	fmt.Fprint(w, connectionsPageScript)
	pageClose(w, r)
}

const connectionsPageScript = `<script>
async function disconnect(p){
  if(!confirm('Disconnect '+p+'?'))return;
  var res=await fetch('/dash/me/connections/'+encodeURIComponent(p),{method:'DELETE'});
  if(res.ok){location.reload();}
  else{var err=document.getElementById('conn-err');err.textContent=await res.text();err.style.display='block';}
}
</script>`

// handleMeConnectionStart mints the CSRF state + PKCE challenge and 302s to the
// provider's authorize URL. POST (state-changing → same-origin CSRF guard).
func (d *dash) handleMeConnectionStart(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.surrogate == nil {
		http.Error(w, "connections unavailable", http.StatusServiceUnavailable)
		return
	}
	provider := r.PathValue("provider")
	if _, ok := d.surrogate.Provider(provider); !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	secure := cookieSecure(r)
	challenge, err := auth.WritePKCE(w, secure)
	if err != nil {
		slog.Warn("me_connections pkce", "sub", sub, "err", err)
		http.Error(w, "pkce failed", http.StatusInternalServerError)
		return
	}
	state := auth.SignState(d.stateSecret, auth.StateIntent{})
	auth.WriteStateCookie(w, state, secure)
	authURL, err := d.surrogate.AuthorizeURL(provider, d.callbackURL(provider), state, challenge)
	if err != nil {
		slog.Warn("me_connections authorize url", "sub", sub, "provider", provider, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("me_connections start", "sub", sub, "provider", provider)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleMeConnectionCallback validates the state, exchanges the code, and
// persists the token as a user-scoped OAuth secret row.
func (d *dash) handleMeConnectionCallback(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if d.surrogate == nil {
		http.Error(w, "connections unavailable", http.StatusServiceUnavailable)
		return
	}
	provider := r.PathValue("provider")
	p, ok := d.surrogate.Provider(provider)
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	if _, valid := auth.VerifyState(d.stateSecret, r); !valid {
		http.Error(w, "invalid or expired state", http.StatusForbidden)
		return
	}
	verifier := auth.ConsumePKCE(w, r, cookieSecure(r))
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	tok, err := d.surrogate.Exchange(r.Context(), provider, code, verifier, d.callbackURL(provider))
	if err != nil {
		slog.Warn("me_connections exchange", "sub", sub, "provider", provider, "err", err)
		http.Error(w, "exchange failed", http.StatusBadGateway)
		return
	}
	ss := d.secretStore()
	if ss == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := ss.PutOAuthSecret(sub, p.SecretKey, tok.Access, provider, tok.Refresh, tok.ExpiresAt, tok.Scope); err != nil {
		slog.Warn("me_connections persist", "sub", sub, "provider", provider, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	emitSecretSet(sub, p.SecretKey)
	slog.Info("me_connections connected", "sub", sub, "provider", provider, "key", p.SecretKey)
	http.Redirect(w, r, "/dash/me/connections", http.StatusSeeOther)
}

// handleMeConnectionDelete best-effort revokes then deletes the OAuth row.
func (d *dash) handleMeConnectionDelete(w http.ResponseWriter, r *http.Request) {
	sub, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.surrogate == nil {
		http.Error(w, "connections unavailable", http.StatusServiceUnavailable)
		return
	}
	provider := r.PathValue("provider")
	p, ok := d.surrogate.Provider(provider)
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	ss := d.secretStore()
	if ss == nil {
		http.Error(w, "secrets store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Best-effort provider-side revoke before deleting the local row.
	if conns, err := ss.ListUserConnections(sub); err == nil {
		for _, c := range conns {
			if c.Provider == provider && c.Value != "" {
				if rerr := d.surrogate.Revoke(context.Background(), provider, c.Value); rerr != nil {
					slog.Warn("me_connections revoke", "sub", sub, "provider", provider, "err", rerr)
				}
				break
			}
		}
	}
	switch err := ss.DeleteSecretRow(store.ScopeUser, sub, p.SecretKey); {
	case errors.Is(err, store.ErrSecretNotFound):
		http.Error(w, "not connected", http.StatusNotFound)
		return
	case err != nil:
		slog.Warn("me_connections delete", "sub", sub, "provider", provider, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.delete",
		Actor:    "user:" + sub,
		ActorSub: sub,
		Surface:  audit.SurfaceREST,
		Resource: "secrets/user/" + sub + "/" + p.SecretKey,
		Scope:    "user",
		Outcome:  audit.OutcomeOK,
	})
	slog.Info("me_connections disconnected", "sub", sub, "provider", provider)
	w.WriteHeader(http.StatusNoContent)
}
