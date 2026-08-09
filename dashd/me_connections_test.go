package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth/surrogate"
	_ "modernc.org/sqlite"
)

// connectionsDash wires a dash whose surrogate engine's github TokenURL points at
// the given exchange handler (the mocked GitHub token endpoint). The secrets
// table (with routd 0017's surrogate columns) comes from routd's own chain.
func connectionsDash(t *testing.T, exchange http.HandlerFunc) (*dash, *sql.DB) {
	t.Helper()
	db := routdDB(t)
	srv := httptest.NewServer(exchange)
	t.Cleanup(srv.Close)
	eng := surrogate.NewEngineWith(
		map[string]surrogate.Provider{"github": {
			AuthURL: "https://github.test/authorize", TokenURL: srv.URL,
			SecretKey: "GITHUB_TOKEN", Scopes: []string{"repo"},
		}},
		map[string]surrogate.ClientCreds{"github": {ID: "cid", Secret: "csecret"}},
	)
	d := &dash{
		dbRoutd: db, secretKeyring: [][]byte{[]byte("k")},
		surrogate: eng, stateSecret: []byte("state-secret"), connBaseURL: "https://dash.test",
	}
	return d, db
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Acceptance (a): "Connect GitHub" round-trips — start mints state+PKCE and
// redirects; the callback exchanges the code and persists a user OAuth row with
// provider + non-NULL expires_at + refresh_val.
func TestConnections_ConnectRoundTrip(t *testing.T) {
	d, db := connectionsDash(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "the-code" {
			t.Errorf("exchange form = %v", r.Form)
		}
		if r.Form.Get("code_verifier") == "" {
			t.Error("PKCE code_verifier missing from exchange")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gh-access","refresh_token":"gh-refresh","expires_in":3600,"scope":"repo"}`))
	})
	mux := newMux(d)

	// 1. start → 302 to the provider authorize URL + state/PKCE cookies.
	start := httptest.NewRequest("POST", "/dash/me/connections/github/start", nil)
	start.Header.Set("X-User-Sub", "github:alice")
	sw := httptest.NewRecorder()
	mux.ServeHTTP(sw, start)
	if sw.Code != http.StatusFound {
		t.Fatalf("start = %d body=%q", sw.Code, sw.Body.String())
	}
	cookies := sw.Result().Cookies()
	stateCookie := cookieByName(cookies, "oauth_state")
	pkceCookie := cookieByName(cookies, "oauth_pkce")
	if stateCookie == nil || pkceCookie == nil {
		t.Fatalf("missing state/pkce cookies: %v", cookies)
	}

	// 2. callback with the same state as query + cookie → 303 back.
	cb := httptest.NewRequest("GET", "/dash/me/connections/github/callback?code=the-code&state="+stateCookie.Value, nil)
	cb.Header.Set("X-User-Sub", "github:alice")
	cb.AddCookie(stateCookie)
	cb.AddCookie(pkceCookie)
	cw := httptest.NewRecorder()
	mux.ServeHTTP(cw, cb)
	if cw.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d body=%q", cw.Code, cw.Body.String())
	}
	if loc := cw.Header().Get("Location"); loc != "/dash/me/connections" {
		t.Errorf("callback redirect = %q", loc)
	}

	// 3. row persisted with provider + non-NULL expires_at + refresh_val.
	var provider string
	var exp, ref sql.NullString
	if err := db.QueryRow(
		`SELECT provider, expires_at, refresh_val FROM secrets
		 WHERE scope_kind='user' AND scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&provider, &exp, &ref); err != nil {
		t.Fatalf("no row persisted: %v", err)
	}
	if provider != "github" {
		t.Errorf("provider = %q, want github", provider)
	}
	if !exp.Valid || !ref.Valid {
		t.Errorf("expires_at/refresh_val NULL after connect: exp=%v ref=%v", exp, ref)
	}
}

// A callback whose state cookie is absent (forged) is rejected before exchange.
func TestConnections_CallbackRejectsBadState(t *testing.T) {
	d, _ := connectionsDash(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("exchange must not run on invalid state")
	})
	mux := newMux(d)
	cb := httptest.NewRequest("GET", "/dash/me/connections/github/callback?code=x&state=forged", nil)
	cb.Header.Set("X-User-Sub", "github:alice")
	cw := httptest.NewRecorder()
	mux.ServeHTTP(cw, cb)
	if cw.Code != http.StatusForbidden {
		t.Fatalf("callback with forged state = %d, want 403", cw.Code)
	}
}

// The list surface (JSON) marks github not-connected before any connect.
func TestConnections_ListJSON(t *testing.T) {
	d, _ := connectionsDash(t, func(w http.ResponseWriter, r *http.Request) {})
	mux := newMux(d)
	req := httptest.NewRequest("GET", "/dash/me/connections", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("list = %d body=%q", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `"provider":"github"`) || !strings.Contains(body, `"connected":false`) {
		t.Errorf("list JSON = %s", body)
	}
}
