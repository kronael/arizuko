package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestMigrateGitHubRouteTopics(t *testing.T) {
	s := openMem(t)
	rows := []core.Route{
		{ID: 1, Seq: 1, Match: "verb=mention chat_jid=github:o/r/pr/*", Target: "team", ObserveWindowChars: 42},
		{ID: 2, Seq: 2, Match: "chat_jid=github:o/r/pr/*", Target: "team?observe"},
		{ID: 3, Seq: 3, Match: "chat_jid=github:o/r/pr/*", Target: "team#fixed"},
		{ID: 4, Seq: 4, Match: "chat_jid=slack:team", Target: "team"},
		{ID: 5, Seq: 5, Match: "platform=github verb=mention", Target: "desk"},
		{ID: 6, Seq: 6, Match: "verb=mention platform=github", Target: "desk?observe"},
		{ID: 7, Seq: 7, Match: "platform=github", Target: "desk#fixed"},
		{ID: 8, Seq: 8, Match: "platform=github-enterprise", Target: "desk"},
	}
	for _, row := range rows {
		if _, err := s.PutRouteRow(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MigrateGitHubRouteTopics(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, target := range []string{"team#{jid}", "team?observe#{jid}", "team#fixed", "team", "desk#{jid}", "desk?observe#{jid}", "desk#fixed", "desk"} {
		row, ok, err := s.RouteByID(int64(i + 1))
		if err != nil || !ok || row.Target != target || row.Seq != rows[i].Seq || row.ObserveWindowChars != rows[i].ObserveWindowChars {
			t.Fatalf("route %d = %+v, %v, %v", i, row, ok, err)
		}
	}
	if _, err := s.PutRouteRow(core.Route{ID: 9, Seq: 9, Match: "platform=github", Target: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateGitHubRouteTopics(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, _, err := s.RouteByID(9)
	if err != nil || row.Target != "team" {
		t.Fatalf("second run = %+v, %v", row, err)
	}
}
