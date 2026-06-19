package tests

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
)

func TestFeature_Grants(t *testing.T) {
	t.Run("crud", func(t *testing.T) {
		s := mustMonolithDB(t)
		row := core.ACLRow{Principal: "alice@x", Action: "admin", Scope: "corp/eng", Effect: "allow"}
		if err := s.AddACLRow(row); err != nil {
			t.Fatal(err)
		}
		scopes := s.UserScopes("alice@x")
		if len(scopes) == 0 {
			t.Fatalf("UserScopes empty after grant")
		}
		if err := s.RemoveACLRow(row); err != nil {
			t.Fatal(err)
		}
		if got := len(s.ListACL("alice@x")); got != 0 {
			t.Fatalf("ACL rows after revoke = %d, want 0", got)
		}
	})

	t.Run("allow-deny-effect", func(t *testing.T) {
		rules := []string{"send", "!send(jid=telegram:*)"}
		if grants.CheckAction(rules, "send", map[string]string{"jid": "telegram:42"}) {
			t.Fatal("deny rule should have blocked telegram send")
		}
		if !grants.CheckAction(rules, "send", map[string]string{"jid": "slack:1"}) {
			t.Fatal("non-telegram send should still be allowed")
		}
	})

	t.Run("param-glob", func(t *testing.T) {
		rules := []string{"delete(jid=web:*)"}
		if !grants.CheckAction(rules, "delete", map[string]string{"jid": "web:demo/x"}) {
			t.Fatal("web jid should match delete(jid=web:*)")
		}
		if grants.CheckAction(rules, "delete", map[string]string{"jid": "telegram:1"}) {
			t.Fatal("telegram jid should NOT match delete(jid=web:*)")
		}
	})

	t.Run("malformed-rule-denies", func(t *testing.T) {
		if grants.CheckAction([]string{"send(jid=telegram:*"}, "send", map[string]string{"jid": "telegram:1"}) {
			t.Fatal("malformed rule must not grant anything")
		}
	})

	t.Run("tier-derivation", func(t *testing.T) {
		root := grants.DeriveRules(nil, "main", 0, "main")
		if len(root) != 1 || root[0] != "*" {
			t.Fatalf("tier 0 rules = %v, want [*]", root)
		}
		deep := grants.DeriveRules(nil, "a/b/c/d", 3, "a")
		if grants.CheckAction(deep, "register_group", nil) {
			t.Fatal("deep tier must NOT have register_group")
		}
		if !grants.CheckAction(deep, "reply", nil) {
			t.Fatal("deep tier should still reply")
		}
	})

	t.Run("rest-acl-scope-gate", func(t *testing.T) {
		f := bootFederation(t)
		body := map[string]string{"principal": "bob@x", "scope": "demo", "action": "admin", "effect": "allow"}
		weak := f.authd.mintService(t, "service:rogue", "chats:read")
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/acl", weak, "", body); rec.StatusCode != 403 {
			t.Fatalf("acl write without acl:write = %d, want 403", rec.StatusCode)
		}
		strong := f.authd.mintService(t, "service:dashd", "acl:write")
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/acl", strong, "", body); rec.StatusCode != 200 {
			t.Fatalf("acl write with acl:write = %d, want 200", rec.StatusCode)
		}
	})
}
