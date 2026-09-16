package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/core"
)

// fakeDex is the slice of Dex a login touches; its token endpoint redeems any code for claims.
type fakeDex struct {
	*httptest.Server
	key      *rsa.PrivateKey
	claims   map[string]any
	tokenReq url.Values
}

func newFakeDex(t *testing.T) *fakeDex {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDex{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": f.URL, "authorization_endpoint": f.URL + "/auth",
			"token_endpoint": f.URL + "/token", "jwks_uri": f.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.tokenReq = r.Form
		writeJSON(w, map[string]any{"id_token": f.sign(t), "token_type": "bearer"})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDex) sign(t *testing.T) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(f.claims)
	if err != nil {
		t.Fatal(err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// idClaims is what Dex v2.45.1 put on the ID token in the spike (spec 6/22 step 4).
func idClaims(f *fakeDex) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": f.URL, "sub": "ChI4MTEyOTU2NzA3MDIwNDcyNzISB2Rpc2NvcmQ", "aud": "arizuko",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"name": "Kron Ael", "preferred_username": "kronael",
		"federated_claims": map[string]any{"connector_id": "discord", "user_id": "811295670702047272"},
	}
}

func dexCfg(f *fakeDex) *core.Config {
	return &core.Config{
		AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example",
		DexIssuer: f.URL, DexClientID: "arizuko", DexSecret: "spike-secret",
	}
}

// dexLogin runs the browser's two hops: the redirect writes the cookies the callback presents.
func dexLogin(t *testing.T, o *oauth) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	o.redirect("dex")(rec, httptest.NewRequest("GET", "/auth/dex", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status %d: %s", rec.Code, rec.Body.String())
	}
	state := cookieValue(rec.Result().Cookies(), "oauth_state")
	cb := httptest.NewRequest("GET", "/auth/dex/callback?code=abc&state="+url.QueryEscape(state), nil)
	cb.Header.Set("Accept", "application/json")
	for _, c := range rec.Result().Cookies() {
		cb.AddCookie(c)
	}
	out := httptest.NewRecorder()
	o.dexCallback(out, cb)
	return out
}

func TestDexRedirectAsksForFederatedID(t *testing.T) {
	f := newFakeDex(t)
	o := newOAuth(t, newTestAuthd(t, testDB(t)), dexCfg(f), nil)

	rec := httptest.NewRecorder()
	o.redirect("dex")(rec, httptest.NewRequest("GET", "/auth/dex", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), f.URL+"/auth?") {
		t.Fatalf("redirect must land on the discovered authorization endpoint: %s", loc)
	}
	q := loc.Query()
	want := map[string]string{
		"client_id": "arizuko", "redirect_uri": "https://auth.example/auth/dex/callback",
		"response_type": "code", "scope": "openid profile federated:id", "code_challenge_method": "S256",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q want %q", k, q.Get(k), v)
		}
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Errorf("redirect must carry state + PKCE challenge: %s", loc)
	}
}

// The principal is <connector_id>:<user_id>, so one grant row serves either login path.
func TestDexCallbackMintsTheNativeProviderPrincipal(t *testing.T) {
	f := newFakeDex(t)
	f.claims = idClaims(f)
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, dexCfg(f), nil)

	out := dexLogin(t, o)
	if out.Code != http.StatusOK {
		t.Fatalf("callback status %d: %s", out.Code, out.Body.String())
	}
	sub, err := auth.VerifyToken(jsonField(t, out.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatalf("Dex-mapped session must verify as an authd token: %v", err)
	}
	if sub.Sub != "user:discord:811295670702047272" {
		t.Fatalf("sub = %q want user:discord:811295670702047272", sub.Sub)
	}
	if !userExists(t, db, "discord:811295670702047272") {
		t.Fatal("canonical user must be keyed on the provider principal, not Dex's sub")
	}
	if got := f.tokenReq; got.Get("code") != "abc" || got.Get("code_verifier") == "" ||
		got.Get("client_secret") != "spike-secret" ||
		got.Get("redirect_uri") != "https://auth.example/auth/dex/callback" {
		t.Fatalf("token request must carry code, PKCE verifier, secret and redirect: %v", got)
	}
}

func TestDexCallbackRejectsWhatIsNotOurs(t *testing.T) {
	cases := map[string]func(map[string]any){
		"another client's token": func(c map[string]any) { c["aud"] = "someone-else" },
		"another issuer":         func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":                func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"no federated_claims":    func(c map[string]any) { delete(c, "federated_claims") },
		// Without the connector id dispatch would create the account ":811…".
		"no connector id": func(c map[string]any) {
			c["federated_claims"] = map[string]any{"user_id": "811295670702047272"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeDex(t)
			f.claims = idClaims(f)
			mutate(f.claims)
			db := testDB(t)
			o := newOAuth(t, newTestAuthd(t, db), dexCfg(f), nil)

			out := dexLogin(t, o)
			if out.Code != http.StatusBadGateway {
				t.Fatalf("status %d want 502: %s", out.Code, out.Body.String())
			}
			if users := entityCount(t, db, authstore.AuthUsers); users != 0 {
				t.Fatalf("a rejected token must not create a user (rows=%d)", users)
			}
		})
	}
}

// With DEX_* unset nothing changes: no route, no button.
func TestDexRoutesMountOnlyWhenConfigured(t *testing.T) {
	f := newFakeDex(t)
	s := &server{a: newTestAuthd(t, testDB(t))}
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GoogleClientID: "gid"}

	get := func(m *http.ServeMux, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	m := s.mux(cfg)
	if rec := get(m, "/auth/dex"); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured /auth/dex = %d want 404", rec.Code)
	}
	if body := get(m, "/auth/login").Body.String(); strings.Contains(body, "/auth/dex") {
		t.Fatal("unconfigured login page must not offer Dex")
	}

	cfg.DexIssuer, cfg.DexClientID = f.URL, "arizuko"
	m = s.mux(cfg)
	if rec := get(m, "/auth/dex"); rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("configured /auth/dex = %d want 307: %s", rec.Code, rec.Body.String())
	}
	if body := get(m, "/auth/login").Body.String(); !strings.Contains(body, `href="/auth/dex"`) {
		t.Fatal("configured login page must offer Dex")
	}
}
