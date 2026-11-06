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
		rows, err := s.ListACL("")
		if err != nil || len(rows) != 0 {
			t.Fatalf("seeded acl rows = %+v, %v", rows, err)
		}
	})
	t.Run("existing acl", func(t *testing.T) {
		s := openEntities(t)
		if err := s.AddACLRow(core.ACLRow{Principal: "person", Action: "routes:get", Scope: "**", Effect: EffectAllow}, ""); err != nil {
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
