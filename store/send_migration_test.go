package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestMigrateMemberSend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		grantedBy   string
		grantOption bool
		removed     bool
	}{
		{"seeded", "system:4r", false, true},
		{"operator", "user:operator", false, false},
		{"delegable", "system:4r", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openMem(t)
			row := core.ACLRow{
				Principal: RoleMember, Action: "mcp:send", Scope: "**",
				Effect: EffectAllow, GrantedBy: tc.grantedBy, GrantOption: tc.grantOption,
			}
			other := core.ACLRow{
				Principal: "folder:world/a", Action: "mcp:send", Scope: "world/b",
				Effect: EffectAllow, GrantedBy: "user:operator",
			}
			if err := s.PutACLRow(row); err != nil {
				t.Fatal(err)
			}
			if err := s.PutACLRow(other); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.MigrateMemberSend(context.Background()); err != nil {
					t.Fatal(err)
				}
				rows, err := s.ACLRowsFor([]string{RoleMember, other.Principal})
				if err != nil {
					t.Fatal(err)
				}
				found, foundOther := false, false
				for _, got := range rows {
					if got.Principal == row.Principal && got.Action == row.Action {
						found = true
					}
					if got.Principal == other.Principal && got.Scope == other.Scope {
						foundOther = true
					}
				}
				wantFound := !tc.removed || i == 1
				if found != wantFound || !foundOther {
					t.Fatalf("run %d: rows %+v", i, rows)
				}
				if i == 0 && tc.removed {
					if err := s.PutACLRow(row); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
