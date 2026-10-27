package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestSeedChatSettingsGrantsOnlyOnEmptyACL(t *testing.T) {
	ctx := context.Background()
	t.Run("fresh and revoked", func(t *testing.T) {
		s := openEntities(t)
		if err := SeedRoutdFloor(ctx, s.Obj()); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListACL("service:dashd")
		if err != nil || len(rows) != 2 {
			t.Fatalf("service rows = %+v, %v", rows, err)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			if row.Scope != "**" || row.Effect != EffectAllow || row.GrantOption || row.GrantedBy != "system" || row.GrantedAt != core.TS(chatSettingsGrantedAt) {
				t.Fatalf("seed row = %+v", row)
			}
			seen[row.Action] = true
		}
		if !seen["chats:get"] || !seen["chats:update"] {
			t.Fatalf("actions = %+v", seen)
		}
		if err := s.RemoveACLRow(rows[0]); err != nil {
			t.Fatal(err)
		}
		if err := SeedRoutdFloor(ctx, s.Obj()); err != nil {
			t.Fatal(err)
		}
		left, err := s.ListACL("service:dashd")
		if err != nil || len(left) != 1 || left[0].Action == rows[0].Action {
			t.Fatalf("revoked grant restored: %+v, %v", left, err)
		}
	})
	t.Run("existing acl", func(t *testing.T) {
		s := openEntities(t)
		if err := s.AddACLRow(core.ACLRow{Principal: "person", Action: "routes:get", Scope: "**", Effect: EffectAllow}); err != nil {
			t.Fatal(err)
		}
		if err := SeedRoutdFloor(ctx, s.Obj()); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListACL("service:dashd")
		if err != nil || len(rows) != 0 {
			t.Fatalf("existing acl gained grants: %+v, %v", rows, err)
		}
	})
}
