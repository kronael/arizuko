package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestRouteKeyValidatesTarget(t *testing.T) {
	for _, target := range []string{"team?bad", "team#{unknown}"} {
		if _, err := routeKey(core.Route{Target: target, Match: "platform=slack"}); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}

func TestRouteWritersRejectInvalidTargets(t *testing.T) {
	s := openMem(t)
	id, err := s.AddRoute(core.Route{Seq: 1, Target: "team", Match: "platform=slack"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"team?unknown", "team#{unknown}"} {
		r := core.Route{Seq: 2, Target: target, Match: "platform=slack"}
		if _, err := s.AddRoute(r); err == nil {
			t.Errorf("AddRoute accepted %q", target)
		}
		if _, err := s.PutRouteRow(r); err == nil {
			t.Errorf("PutRouteRow accepted %q", target)
		}
		if _, err := s.UpdateRoute(id, r); err == nil {
			t.Errorf("UpdateRoute accepted %q", target)
		}
		if _, err := s.PutRouteInto(context.Background(), &WriteSet{}, r, "", "", nil); err == nil {
			t.Errorf("PutRouteInto accepted %q", target)
		}
		if _, err := routeCreateWrite(r, "", ""); err == nil {
			t.Errorf("seed accepted %q", target)
		}
	}
}
