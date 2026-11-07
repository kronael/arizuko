package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestMigrateRouteModes(t *testing.T) {
	s := openMem(t)
	for i, target := range []string{"team#observe", "team#engaged", "team#announce", "team#issues", "team#topic"} {
		_, err := s.PutRouteRow(core.Route{ID: int64(i + 1), Seq: i, Match: "platform=slack", Target: target, ObserveWindowChars: 42})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MigrateRouteModes(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, target := range []string{"team?observe", "team?engaged", "team?announce", "team?issues", "team#topic"} {
		r, ok, err := s.RouteByID(int64(i + 1))
		if err != nil || !ok || r.Target != target || r.Seq != i || r.ObserveWindowChars != 42 {
			t.Fatalf("route %d = %+v, %v, %v", i, r, ok, err)
		}
	}
	if _, err := s.PutRouteRow(core.Route{ID: 6, Seq: 6, Match: "platform=slack", Target: "team#observe"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateRouteModes(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.RouteByID(6)
	if err != nil || r.Target != "team#observe" {
		t.Fatalf("second run: %+v, %v", r, err)
	}
}
