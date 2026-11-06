package auth_test

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestPolicyLayers(t *testing.T) {
	s := openMem(t)
	c := auth.Caller{Principal: "folder:w"}
	for _, role := range []string{store.RoleWorld, store.RoleMember} {
		if err := s.AddMembership(c.Principal, role, "test"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(tool string, params map[string]string, want bool) {
		t.Helper()
		if got := mustHold(t, s, c, tool, "w", params); got != want {
			t.Fatalf("%s %v held = %v, want %v", tool, params, got, want)
		}
	}
	check("reply", nil, false)
	check("schedule_task", nil, true)
	if err := s.RemoveMembership(c.Principal, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	check("reply", nil, true)
	if err := s.AddMembership(c.Principal, store.RoleMember, "test"); err != nil {
		t.Fatal(err)
	}
	addRow(t, s, c.Principal, auth.HoldPrefix+"reply", "w", store.EffectAllow)
	check("reply", nil, true)
	check("like", nil, false)
	addRowFull(t, s, core.ACLRow{Principal: c.Principal, Action: auth.HoldPrefix + "like", Scope: "w", Effect: store.EffectDeny, Params: "reaction=👀"})
	check("like", map[string]string{"reaction": "👀"}, false)
	check("like", map[string]string{"reaction": "❤️"}, false)
}

func TestPolicyDefaultsAndIsolation(t *testing.T) {
	s := openMem(t)
	if !auth.Authorize(s, auth.Caller{Principal: "service:dashd"}, "chats:update", "any/folder", nil) {
		t.Fatal("dashd default missing")
	}
	if err := s.AddMembership("user:operator", store.RoleOperator, "test"); err != nil {
		t.Fatal(err)
	}
	c := auth.Caller{Principal: "user:operator"}
	if !auth.Authorize(s, c, "admin", "**", nil) {
		t.Fatal("operator default missing")
	}
	if mustHold(t, s, c, "reply", "w", nil) {
		t.Fatal("operator wildcard holds reply")
	}
	if keys := auth.ShellEnvKeys(s, c, "w", []string{"SECRET"}); len(keys) != 0 {
		t.Fatalf("operator wildcard grants secrets: %v", keys)
	}
	if err := authDelegateForTest(s, c.Principal, []core.ACLRow{{Principal: "user:other", Action: "mcp:send", Scope: "w", Effect: store.EffectAllow}}); err != nil {
		t.Fatalf("operator cannot delegate: %v", err)
	}
}

func TestLocalHoldOverridesDefaultSkips(t *testing.T) {
	s := openMem(t)
	c := auth.Caller{Principal: "folder:w"}
	for _, role := range []string{store.RoleWorld, store.RoleMember} {
		if err := s.AddMembership(c.Principal, role, "test"); err != nil {
			t.Fatal(err)
		}
	}
	addRow(t, s, c.Principal, auth.HoldPrefix+"*", "w", store.EffectAllow)
	for _, tool := range auth.SkipApprovalTools() {
		if tool != "delete" {
			addRow(t, s, c.Principal, auth.HoldPrefix+tool, "w", store.EffectDeny)
		}
	}
	if !mustHold(t, s, c, "delete", "w", nil) || mustHold(t, s, c, "reply", "w", nil) {
		t.Fatal("local hold and skips did not decide")
	}
}

func TestPolicyRefusesHeldScope(t *testing.T) {
	s := openMem(t)
	c := auth.Caller{Principal: "service:dashd"}
	if !auth.Authorize(s, c, "chats:update", "w", nil) {
		t.Fatal("default grant missing")
	}
	_, err := objstore.Replace(context.Background(), s.Obj(), store.OwnerScope(store.OwnerRoutd), "", objstore.Claim{Intent: "test"}, func(context.Context, *objstore.Replacer) error {
		if auth.Authorize(s, c, "chats:update", "w", nil) {
			t.Error("held scope authorized")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func authDelegateForTest(s *store.Store, granter string, rows []core.ACLRow) error {
	mutations := make([]auth.Mutation, len(rows))
	for i, row := range rows {
		mutations[i] = auth.Mutation{Row: row}
	}
	_, err := auth.CheckGrant(s, auth.Caller{Principal: granter}, auth.Ask{}, mutations...)
	return err
}
