package resources

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestACLApplyRejectsBareToolAction(t *testing.T) {
	db := openMem(t)
	checksum := routdChecksum(t, db)
	_, err := resreg.Apply(t.Context(), resreg.On(db), resreg.SubsystemRoutd,
		checksum, false, map[string]any{
			"acl": []ACLRow{{Principal: "bob", Scope: "team", Action: "send"}},
		}, nil)
	if err == nil || !strings.Contains(err.Error(), "action") {
		t.Errorf("bare tool accepted: %v", err)
	}
	if rows, err := store.NewOver(db).ListACL("bob"); err != nil || len(rows) != 0 {
		t.Errorf("refused action persisted: %+v, %v", rows, err)
	}
	if got := routdChecksum(t, db); got != checksum {
		t.Error("refused action changed the manifest")
	}
}
