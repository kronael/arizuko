package routd

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestNewGroupIsBornWithTheFloor pins the 5/33-paths-roles floor as DATA on the one path
// that writes it: PutGroup writes the role:member edge, so every folder holds
// the twelve messaging verbs from the instant it exists.
//
// A folder that is a member of nothing gets an empty auth.EffectiveActions and
// routd's per-turn Visible view then advertises ZERO MCP tools. The agent still
// answers — prose rides submit_turn, not the reply tool — which is why that
// state reads as "the agent works" while send_file, edit, like and the rest are
// simply absent. Measured on krons: folder:krons held mcp:reply/send/send_file/
// send_voice/edit all false while the operator principal held them via
// role:operator.
func TestNewGroupIsBornWithTheFloor(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open routd.db: %v", err)
	}
	defer db.Close()

	if err := db.PutGroup(core.Group{Folder: "fresh"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	held := auth.EffectiveActions(store.NewOver(db.Obj()), auth.Caller{Principal: "folder:fresh"})
	if !held("mcp:send_file") {
		t.Fatal("a newly created group does not hold the messaging floor")
	}
}
