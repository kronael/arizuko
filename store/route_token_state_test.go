package store

import (
	"fmt"
	"github.com/kronael/arizuko/objstore"
	"testing"
)

func TestRouteTokenTargetReadsOnlyMatchingKeys(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for i := range 40 {
		if err := s.InsertRouteToken(fmt.Sprint(i), RouteToken{JID: fmt.Sprintf("web:noise%d", i), OwnerFolder: "world", TargetFolder: "world"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertRouteToken("wanted", RouteToken{JID: "web:world/chat", OwnerFolder: "world", TargetFolder: "world"}); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	got, err := RouteTokenTargetByJID(t.Context(), c, "web:world/chat")
	if err != nil || got != "world" || c.rows != 2 {
		t.Fatalf("target=%s rows=%d err=%v", got, c.rows, err)
	}
	if ok, err := s.RevokeRouteToken("web:world/chat", "world"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	c.rows = 0
	if got, err = RouteTokenTargetByJID(t.Context(), c, "web:world/chat"); err != nil || got != "" || c.rows != 0 {
		t.Fatalf("revoked=%s rows=%d err=%v", got, c.rows, err)
	}
}
func TestRouteTokenStateMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	w, err := routeTokenPut("legacy", RouteToken{JID: "web:world/chat", OwnerFolder: "world", TargetFolder: "world"}, RouteTokenKindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{w}); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := RouteTokenTargetByJID(t.Context(), s.Obj(), "web:world/chat"); err != nil || got != "world" {
		t.Fatalf("target %s %v", got, err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if err := NewOver(c).MigrateStateIndexes(t.Context()); err != nil || c.rows != 12 {
		t.Fatalf("repeat rows=%d err=%v", c.rows, err)
	}
}
