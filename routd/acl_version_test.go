package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestACLWriteProvesDoorVersion(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	grantMCPTools(t, db, "a", "add_acl")
	grantWithOption(t, db, "folder:a", "mcp:send", "a", "")
	s := &Server{db: db}
	ws := &store.WriteSet{}
	x := resreg.Execution{Action: resreg.Action("add"), Args: resreg.Args{"principal": "bob", "action": "mcp:send", "scope": "a"}, Writes: ws}
	check := func(_ resreg.Execution, m auth.Mutation) (objstore.Version, error) {
		v, err := auth.CheckGrant(db.store(), auth.Caller{Route: "a"}, auth.Ask{Action: "mcp:add_acl", Scope: "a"}, m)
		if err != nil {
			return v, err
		}
		err = db.store().AddACLRow(core.ACLRow{Principal: "folder:a", Action: "mcp:add_acl", Scope: "a", Effect: "deny"}, "")
		return v, err
	}
	if _, err := s.aclHandler(context.Background(), x, check); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(context.Background(), ws); !errors.Is(err, objstore.ErrScopeMoved) {
		t.Fatalf("write: %v", err)
	}
}
