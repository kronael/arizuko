package store

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

func TestMigrateGitHubSourceRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, source, owner, edit, ids string
		current                        bool
	}{
		{name: "app", source: "app/sloth", owner: "sloth/discord", ids: "[9,10,11,12,13,14,15,16]"},
		{name: "repo", source: "repo/vumilan/morha", owner: "sloth/morha", ids: "[7,8,9,10,11,12]"},
		{name: "edited", source: "app/sloth", owner: "sloth/discord", edit: "target"},
		{name: "missing", source: "app/sloth", owner: "sloth/discord", edit: "missing"},
		{name: "ambiguous", source: "app/sloth", owner: "sloth/discord", edit: "duplicate"},
		{name: "current", source: "app/sloth", owner: "sloth/discord", ids: "[9,10,11,12,13,14,15,16]", current: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := keyedStore(t)
			ctx := context.Background()
			foreign, err := GitHubSourceRoutes(tc.source, "another/tenant")
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range foreign {
				if _, err := s.AddRoute(route); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AddGitHubSource(GitHubSource{Source: tc.source, OwnerFolder: tc.owner}, tc.owner); err != nil {
				t.Fatal(err)
			}
			key, err := githubSourceKey(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.current {
				if err := s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: GitHubSources,
					Key: key, Remove: []string{attrGHTargetFolder, attrGHRouteIDs}}); err != nil {
					t.Fatal(err)
				}
			}
			before, _, err := s.githubSourceItem(ctx, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			route, _, err := s.RouteByID(int64(len(foreign) + 1))
			if err != nil {
				t.Fatal(err)
			}
			switch tc.edit {
			case "target":
				route.Target = tc.owner + "/child#{jid}"
				_, err = s.UpdateRoute(route.ID, route, "")
			case "missing":
				_, err = s.DeleteRoute(route.ID, "")
			case "duplicate":
				route.ID, route.Seq = 0, 999
				_, err = s.AddRoute(route)
			}
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			want := maps.Clone(before.Attrs)
			if tc.ids != "" {
				want[attrGHTargetFolder], want[attrGHRouteIDs] = tc.owner, tc.ids
			}
			for range 2 {
				if err := s.MigrateGitHubSourceRoutes(ctx); err != nil {
					t.Fatal(err)
				}
				after, ok, err := s.githubSourceItem(ctx, tc.source)
				if err != nil || !ok || !reflect.DeepEqual(after.Attrs, want) {
					t.Fatalf("source after migration = %+v, %v, %v; want %+v", after.Attrs, ok, err, want)
				}
			}
			wantLogs := 0
			if tc.ids == "" {
				wantLogs = 1
				if !strings.Contains(logs.String(), "reason=") {
					t.Fatalf("skip has no reason: %s", logs.String())
				}
			}
			if n := strings.Count(logs.String(), "github source ownership migration skipped"); n != wantLogs {
				t.Fatalf("skip logs = %d, want %d: %s", n, wantLogs, logs.String())
			}
			if tc.ids != "" {
				if hit, err := s.RemoveGitHubSource(tc.source, tc.owner); err != nil || !hit {
					t.Fatalf("remove migrated source = %v, %v", hit, err)
				}
				remaining := s.AllRoutes()
				if len(remaining) != len(foreign) {
					t.Fatalf("remaining routes = %+v; want only foreign routes", remaining)
				}
				for _, route := range remaining {
					if route.ID > int64(len(foreign)) {
						t.Fatalf("source route remains: %+v", route)
					}
				}
			}
		})
	}
}
