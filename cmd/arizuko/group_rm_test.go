package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type fakeRuned struct {
	srv *httptest.Server
	mu  sync.Mutex

	stopped       []string
	stillResolved map[string]bool
}

func newFakeRuned(t *testing.T, s *store.Store) *fakeRuned {
	t.Helper()
	f := &fakeRuned{stillResolved: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /v1/runs/stop", func(w http.ResponseWriter, r *http.Request) {
		var req runedv1.StopRunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped = append(f.stopped, req.Folder)
		_, f.stillResolved[req.Folder] = s.GroupByFolder(req.Folder)
		_ = json.NewEncoder(w).Encode(runedv1.StopRunResponse{})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func seedGroups(t *testing.T, s *store.Store, folders ...string) {
	t.Helper()
	for _, folder := range folders {
		if err := s.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
			t.Fatalf("seed %s: %v", folder, err)
		}
	}
}

func hasGroup(s *store.Store, folder string) bool {
	_, ok := s.GroupByFolder(folder)
	return ok
}

// `group rm eval` with child `eval/child` and a reachable runed -> both rows
// are gone and runed got one stop per folder, each after its row was removed.
func TestGroupRmStopsEachFolderAfterThePurge(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child", "keeper")
	runed := newFakeRuned(t, s)
	client := runedv1.NewClient(runed.srv.URL, "", time.Second)

	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, client, "eval", false, &out); err != nil {
		t.Fatalf("runGroupRm: %v", err)
	}

	slices.Sort(runed.stopped)
	if !slices.Equal(runed.stopped, []string{"eval", "eval/child"}) {
		t.Fatalf("stopped = %v, want [eval eval/child]", runed.stopped)
	}
	for folder, resolved := range runed.stillResolved {
		if resolved {
			t.Errorf("%s still had a groups row when runed was asked to stop it", folder)
		}
	}
	if !hasGroup(s, "keeper") || hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Errorf("rows after rm: keeper=%v eval=%v eval/child=%v, want true false false",
			hasGroup(s, "keeper"), hasGroup(s, "eval"), hasGroup(s, "eval/child"))
	}
}

// Runed unreachable and no -f -> the command refuses, names RUNED_URL and -f,
// and removes no row.
func TestGroupRmRefusesWhenRunedIsUnreachable(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	client := runedv1.NewClient(url, "", time.Second)

	var out bytes.Buffer
	err := runGroupRm(context.Background(), s, client, "eval", false, &out)

	if err == nil {
		t.Fatal("runGroupRm = nil, want a refusal")
	}
	for _, want := range []string{"RUNED_URL", "-f/--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if !hasGroup(s, "eval") || !hasGroup(s, "eval/child") {
		t.Error("a refused rm removed rows")
	}
}

// -f -> the rows go and runed receives no request at all, even a reachable one.
func TestGroupRmForceRemovesWithoutStopping(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	runed := newFakeRuned(t, s)

	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, runedv1.NewClient(runed.srv.URL, "", time.Second), "eval", true, &out); err != nil {
		t.Fatalf("runGroupRm -f: %v", err)
	}

	if len(runed.stopped) != 0 {
		t.Errorf("stopped = %v, want none", runed.stopped)
	}
	if hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Error("-f left rows behind")
	}
}

// -f and --force, before or after the folder -> same parse; a missing or extra
// folder, or an unknown flag, errors.
func TestParseGroupRm(t *testing.T) {
	cases := []struct {
		args       []string
		folder     string
		force, bad bool
	}{
		{args: []string{"eval"}, folder: "eval"},
		{args: []string{"-f", "eval"}, folder: "eval", force: true},
		{args: []string{"eval", "--force"}, folder: "eval", force: true},
		{args: []string{}, bad: true},
		{args: []string{"a", "b"}, bad: true},
		{args: []string{"--no-stop", "eval"}, bad: true},
	}
	for _, tc := range cases {
		folder, force, err := parseGroupRm(tc.args)
		if tc.bad {
			if err == nil {
				t.Errorf("parseGroupRm(%v) = nil error, want error", tc.args)
			}
			continue
		}
		if err != nil || folder != tc.folder || force != tc.force {
			t.Errorf("parseGroupRm(%v) = (%q, %v, %v), want (%q, %v, nil)",
				tc.args, folder, force, err, tc.folder, tc.force)
		}
	}
}
