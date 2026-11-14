package resources

import (
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestApplyACLWildcardIndex(t *testing.T) {
	raw := openMem(t)
	s := store.NewOver(raw)
	backend := resreg.On(raw)
	rows := []ACLRow{{Principal: "google:*", Action: "interact", Scope: "world", Effect: store.EffectAllow}}
	apply := func() {
		t.Helper()
		if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemRoutd, routdChecksum(t, raw), false, map[string]any{ACLName: rows}, nil); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	got, err := s.ACLWildcardRows()
	if err != nil || len(got) != 1 || got[0].Effect != store.EffectAllow {
		t.Fatalf("allow %v %v", got, err)
	}
	rows[0].Effect = store.EffectDeny
	apply()
	got, err = s.ACLWildcardRows()
	if err != nil || len(got) != 1 || got[0].Effect != store.EffectDeny {
		t.Fatalf("deny %v %v", got, err)
	}
	rows = nil
	apply()
	got, err = s.ACLWildcardRows()
	if err != nil || len(got) != 0 {
		t.Fatalf("revoke %v %v", got, err)
	}
}
