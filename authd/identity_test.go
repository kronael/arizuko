package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedIdentity inserts a canonical identity + its sub claims directly into
// auth.db (raw INSERTs avoid store.LinkSub's audit_log dependency — the
// endpoint reads via store.GetIdentityForSub regardless of write path).
func seedIdentity(t *testing.T, a *Authd, id, name string, subs ...string) {
	t.Helper()
	if _, err := a.db.Exec(
		`INSERT INTO identities(id, name, created_at) VALUES(?,?,?)`,
		id, name, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	for _, sub := range subs {
		if _, err := a.db.Exec(
			`INSERT INTO identity_claims(sub, identity_id, claimed_at) VALUES(?,?,?)`,
			sub, id, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed claim %s: %v", sub, err)
		}
	}
}

// TestIdentityEndpoint_Resolves: GET /v1/identities/{sub} returns the canonical
// identity + all claimed subs from auth.db (authd OWNS identity — spec 5/9),
// gated on identity:read (service:routd carries it).
func TestIdentityEndpoint_Resolves(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	seedIdentity(t, a, "idn-alice", "alice", "tg:42", "discord:7")

	tok, _ := a.MintForSubject("service:routd", "service", nil, serviceGrants["service:routd"], "")
	resp := doGet(t, ts.URL+"/v1/identities/tg:42", tok)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("identity = %d want 200", resp.StatusCode)
	}
	var out struct {
		Identity *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"identity"`
		Subs []string `json:"subs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Identity == nil || out.Identity.Name != "alice" {
		t.Fatalf("identity = %v want name=alice", out.Identity)
	}
	if len(out.Subs) != 2 {
		t.Fatalf("subs = %v want 2", out.Subs)
	}
}

// TestIdentityEndpoint_Unclaimed: an unclaimed sub returns 200
// {"identity":null,"subs":[]} (not 404) so inspect_identity renders the
// unclaimed shape directly.
func TestIdentityEndpoint_Unclaimed(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	tok, _ := a.MintForSubject("service:routd", "service", nil, serviceGrants["service:routd"], "")
	resp := doGet(t, ts.URL+"/v1/identities/tg:999", tok)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("unclaimed = %d want 200", resp.StatusCode)
	}
	var out struct {
		Identity *json.RawMessage `json:"identity"`
		Subs     []string         `json:"subs"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Identity != nil {
		t.Fatalf("unclaimed identity = %v want null", out.Identity)
	}
}

// TestIdentityEndpoint_RequiresScope: a token without identity:read is 403.
func TestIdentityEndpoint_RequiresScope(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()
	seedIdentity(t, a, "idn-alice", "alice", "tg:42")

	tok, _ := a.MintForSubject("user:u", "user", nil, []string{"routes:read"}, "")
	resp := doGet(t, ts.URL+"/v1/identities/tg:42", tok)
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("no identity:read = %d want 403", resp.StatusCode)
	}
}

// TestIdentityEndpoint_RequiresBearer: no bearer is 401.
func TestIdentityEndpoint_RequiresBearer(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	resp := doGet(t, ts.URL+"/v1/identities/tg:42", "")
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no bearer = %d want 401", resp.StatusCode)
	}
}

// doGet issues a GET with an optional bearer.
func doGet(t *testing.T, url, tok string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
