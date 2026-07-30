// Webhook / route-token feature tests: issue, resolve, revoke, HTTP create
// gates, label validation, and list. Tokens are the platform's single-use
// link between external callers and a routd JID.
package tests

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestFeature_RouteTokens(t *testing.T) {
	// Raw token round-trips to its bound JID and owner; raw is returned once only.
	t.Run("issue-and-resolve", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw, _, err := db.IssueRouteToken("web:demo", "demo", "")
		if err != nil {
			t.Fatal(err)
		}
		jid, owner, _, err := db.ResolveRouteToken(raw)
		if err != nil || jid != "web:demo" || owner != "demo" {
			t.Fatalf("resolve = (%q,%q,%v)", jid, owner, err)
		}
	})

	// Revoking all tokens for a JID makes the raw token unresolvable.
	t.Run("revoke", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw, _, _ := db.IssueRouteToken("hook:demo/gh", "demo", "")
		n, err := db.RevokeRouteTokens("hook:demo/gh", "demo")
		if err != nil || n != 1 {
			t.Fatalf("revoked = %d err=%v, want 1", n, err)
		}
		if _, _, _, err := db.ResolveRouteToken(raw); err == nil {
			t.Fatal("resolve should fail after revoke")
		}
	})

	// Multiple tokens for the same JID are all listed and all revoked together.
	t.Run("list-and-bulk-revoke", func(t *testing.T) {
		db := mustRoutdDB(t)
		for range 3 {
			if _, _, err := db.IssueRouteToken("hook:demo/ci", "demo", ""); err != nil {
				t.Fatal(err)
			}
		}
		tokens, err := db.ListRouteTokens("demo")
		if err != nil || len(tokens) != 3 {
			t.Fatalf("list = %d err=%v, want 3", len(tokens), err)
		}
		n, _ := db.RevokeRouteTokens("hook:demo/ci", "demo")
		if n != 3 {
			t.Fatalf("bulk revoke = %d, want 3", n)
		}
	})

	// POST /v1/route_tokens/chat mints a web: token, gated by routes:write.
	t.Run("http-create-chat", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write")
		body := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo"}
		rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/chat", tok, "", body)
		if rec.StatusCode != 201 {
			t.Fatalf("chat token create = %d, want 201", rec.StatusCode)
		}
	})

	// Hook source_label with path-traversal chars → 400; clean label → 201.
	t.Run("hook-label-validation", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write")
		bad := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo", SourceLabel: "bad label/../x"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/hook", tok, "", bad); rec.StatusCode != 400 {
			t.Fatalf("illegal label = %d, want 400", rec.StatusCode)
		}
		good := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo", SourceLabel: "github"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/hook", tok, "", good); rec.StatusCode != 201 {
			t.Fatalf("valid label = %d, want 201", rec.StatusCode)
		}
	})

	// Token issued without routes:write is rejected at 403.
	t.Run("http-create-requires-scope", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		weak := f.authd.mintService(t, "service:rogue", "chats:read")
		body := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/chat", weak, "", body); rec.StatusCode != 403 {
			t.Fatalf("no routes:write = %d, want 403", rec.StatusCode)
		}
	})

	// Link context (spec 5/W § link context): mint with context round-trips
	// through resolve and list; a context-less mint stays empty.
	t.Run("issue-with-context", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw, _, err := db.IssueRouteToken("web:demo", "demo", "bug reports; triage, don't chat")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, linkCtx, err := db.ResolveRouteToken(raw); err != nil || linkCtx != "bug reports; triage, don't chat" {
			t.Fatalf("resolve context = (%q,%v)", linkCtx, err)
		}
		if _, _, err := db.IssueRouteToken("hook:demo/gh", "demo", ""); err != nil {
			t.Fatal(err)
		}
		rows, err := db.ListRouteTokens("demo")
		if err != nil || len(rows) != 2 {
			t.Fatalf("list = %d err=%v, want 2", len(rows), err)
		}
		byJID := map[string]string{}
		for _, r := range rows {
			byJID[r.JID] = r.Context
		}
		if byJID["web:demo"] != "bug reports; triage, don't chat" || byJID["hook:demo/gh"] != "" {
			t.Fatalf("list contexts = %+v", byJID)
		}
	})

	// REST mint carries context onto the row (one writer, two faces).
	t.Run("http-create-with-context", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write")
		body := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo",
			Context: "stripe events; summarize daily"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/chat", tok, "", body); rec.StatusCode != 201 {
			t.Fatalf("chat token create = %d, want 201", rec.StatusCode)
		}
		rows, err := f.routdDB.ListRouteTokens("demo")
		if err != nil || len(rows) != 1 || rows[0].Context != "stripe events; summarize daily" {
			t.Fatalf("row = %+v err=%v", rows, err)
		}
	})

	// Adversary F8: the fold must NOT apply the agent depth tier-cap to operator
	// REST — a deep owner_folder (would resolve to tier 3) must still mint.
	t.Run("http-deep-owner-mints", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "acme/eng/sre/oncall"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write")
		body := routdv1.RouteTokenRequest{OwnerFolder: "acme/eng/sre/oncall", TargetFolder: "acme/eng/sre/oncall"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/chat", tok, "", body); rec.StatusCode != 201 {
			t.Fatalf("deep-owner REST mint = %d, want 201 (F8)", rec.StatusCode)
		}
	})

	// Spec 5/16 REST-face fold: list + revoke ride the SAME shared handler as
	// the agent's list_tokens/revoke_token, mounted via resreg.RegisterREST.
	// Issue → GET list ({tokens:[...]}) → DELETE revoke → row gone.
	t.Run("http-list-and-revoke-folded", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write routes:read")
		body := routdv1.RouteTokenRequest{OwnerFolder: "demo", TargetFolder: "demo"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/route_tokens/chat", tok, "", body); rec.StatusCode != 201 {
			t.Fatalf("issue = %d, want 201", rec.StatusCode)
		}
		lrec := postBearer(t, f.routdTS.URL, "GET", "/v1/route_tokens?owner_folder=demo", tok, "", nil)
		if lrec.StatusCode != 200 {
			t.Fatalf("list = %d, want 200", lrec.StatusCode)
		}
		var list struct {
			Tokens []struct {
				JID string `json:"jid"`
			} `json:"tokens"`
		}
		if err := json.NewDecoder(lrec.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		if len(list.Tokens) != 1 || list.Tokens[0].JID != "web:demo" {
			t.Fatalf("list tokens = %+v, want [web:demo]", list.Tokens)
		}
		drec := postBearer(t, f.routdTS.URL, "DELETE", "/v1/route_tokens/web:demo?owner_folder=demo", tok, "", nil)
		if drec.StatusCode != 200 {
			t.Fatalf("revoke = %d, want 200", drec.StatusCode)
		}
		rows, err := f.routdDB.ListRouteTokens("demo")
		if err != nil || len(rows) != 0 {
			t.Fatalf("after revoke rows = %+v err=%v, want 0", rows, err)
		}
	})

	// A web: route token binds anonymous chat to a folder; resolving it yields
	// the web:<folder> JID the portal posts on.
	t.Run("web-chat-jid-binding", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw, _, err := db.IssueRouteToken("web:demo/submissions", "demo", "")
		if err != nil {
			t.Fatal(err)
		}
		jid, owner, _, err := db.ResolveRouteToken(raw)
		if err != nil || jid != "web:demo/submissions" || owner != "demo" {
			t.Fatalf("resolve = (%q,%q,%v)", jid, owner, err)
		}
	})
}

// mustRoutdDB opens an in-memory routd.DB pre-seeded with a "demo" group.
func mustRoutdDB(t *testing.T) *routd.DB {
	t.Helper()
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("seed demo group: %v", err)
	}
	return db
}
