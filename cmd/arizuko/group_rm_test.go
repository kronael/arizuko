package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	dataDir       string
	filesPresent  map[string]bool
}

func newFakeRuned(t *testing.T, s *store.Store) *fakeRuned {
	t.Helper()
	f := &fakeRuned{
		stillResolved: map[string]bool{},
		filesPresent:  map[string]bool{},
	}
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
		if f.dataDir != "" {
			for _, base := range []string{"groups", "web/pub", "web/priv"} {
				path := filepath.Join(f.dataDir, base, req.Folder, "content")
				_, err := os.Stat(path)
				f.filesPresent[path] = err == nil
			}
		}
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

func groupRmConfig(t *testing.T) *core.Config {
	t.Helper()
	dataDir := t.TempDir()
	return &core.Config{
		ProjectRoot: dataDir,
		GroupsDir:   filepath.Join(dataDir, "groups"),
		WebDir:      filepath.Join(dataDir, "web"),
	}
}

func seedGroupRmPaths(t *testing.T, cfg *core.Config, folders []string) []string {
	t.Helper()
	var paths []string
	for _, root := range []string{"groups", "web/pub", "web/priv"} {
		for _, folder := range folders {
			path := filepath.Join(cfg.ProjectRoot, root, folder)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "content"), []byte(folder), 0o600); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
	}
	return paths
}

// `group rm eval` with child `eval/child` and a reachable runed -> both rows
// are gone and runed got one stop per folder, each after its row was removed.
func TestGroupRmStopsEachFolderAfterThePurge(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child", "keeper")
	cfg := groupRmConfig(t)
	paths := seedGroupRmPaths(t, cfg, []string{"eval", "eval/child", "keeper"})
	runed := newFakeRuned(t, s)
	runed.dataDir = cfg.ProjectRoot
	client := runedv1.NewClient(runed.srv.URL, "", time.Second)

	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, cfg, client, "eval", false, &out); err != nil {
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
	for path, present := range runed.filesPresent {
		if !present {
			t.Errorf("%s was removed before runed stopped the run", path)
		}
	}
	if !hasGroup(s, "keeper") || hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Errorf("rows after rm: keeper=%v eval=%v eval/child=%v, want true false false",
			hasGroup(s, "keeper"), hasGroup(s, "eval"), hasGroup(s, "eval/child"))
	}
	for _, path := range paths {
		_, err := os.Stat(path)
		if filepath.Base(path) == "keeper" {
			if err != nil {
				t.Errorf("keeper directory: %v", err)
			}
			continue
		}
		if !os.IsNotExist(err) {
			t.Errorf("removed directory %s still exists: %v", path, err)
		}
		if !strings.Contains(out.String(), "removed "+path+"\n") {
			t.Errorf("output does not name deleted path %s: %s", path, &out)
		}
	}
}

// Runed unreachable and no -f -> the command refuses, names RUNED_URL and -f,
// and removes no row.
func TestGroupRmRefusesWhenRunedIsUnreachable(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	cfg := groupRmConfig(t)
	paths := seedGroupRmPaths(t, cfg, []string{"eval", "eval/child"})
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	client := runedv1.NewClient(url, "", time.Second)

	var out bytes.Buffer
	err := runGroupRm(context.Background(), s, cfg, client, "eval", false, &out)

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
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a refused rm removed %s: %v", path, err)
		}
	}
}

// -f -> the rows go and runed receives no request at all, even a reachable one.
func TestGroupRmForceRemovesWithoutStopping(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	cfg := groupRmConfig(t)
	paths := seedGroupRmPaths(t, cfg, []string{"eval", "eval/child"})
	runed := newFakeRuned(t, s)
	client := runedv1.NewClient(runed.srv.URL, "", time.Second)

	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, cfg, client, "eval", true, &out); err != nil {
		t.Fatalf("runGroupRm -f: %v", err)
	}

	if len(runed.stopped) != 0 {
		t.Errorf("stopped = %v, want none", runed.stopped)
	}
	if hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Error("-f left rows behind")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("-f left %s: %v", path, err)
		}
	}
}

// Invalid paths refuse the purge before any row or file changes.
func TestGroupRmRefusesInvalidPaths(t *testing.T) {
	for _, tc := range []struct{ folder, request string }{
		{".", "."},
		{"../outside", "../outside"},
		{"eval/../../outside", "eval/../../outside"},
		{"eval/.", "eval/."},
		{"eval/../../outside", "eval"},
	} {
		t.Run(tc.folder+" via "+tc.request, func(t *testing.T) {
			s := routdStore(t)
			seedGroups(t, s, tc.folder, "keeper")
			cfg := groupRmConfig(t)
			paths := seedGroupRmPaths(t, cfg, []string{"eval", "keeper"})
			var out bytes.Buffer
			err := runGroupRm(context.Background(), s, cfg, nil, tc.request, true, &out)
			if err == nil {
				t.Fatal("unsafe path accepted")
			}
			if !hasGroup(s, tc.folder) || !hasGroup(s, "keeper") {
				t.Fatal("refused rm removed rows")
			}
			for _, path := range paths {
				if _, err := os.Stat(filepath.Join(path, "content")); err != nil {
					t.Errorf("refused rm changed %s: %v", path, err)
				}
			}
		})
	}
}

// Every target is checked before the purge, including descendant web paths.
func TestGroupRmRefusesOutsideSymlinks(t *testing.T) {
	for _, link := range []string{
		"groups", "groups/eval", "groups/eval/child",
		"web/pub", "web/pub/eval", "web/priv/eval/child",
	} {
		t.Run(link, func(t *testing.T) {
			s := routdStore(t)
			seedGroups(t, s, "eval", "eval/child")
			cfg := groupRmConfig(t)
			paths := seedGroupRmPaths(t, cfg, []string{"eval", "eval/child"})
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "keep")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.ProjectRoot, link)
			if err := os.Rename(path, path+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := runGroupRm(context.Background(), s, cfg, nil, "eval", true, &out)
			if err == nil {
				t.Fatal("outside symlink accepted")
			}
			if !hasGroup(s, "eval") || !hasGroup(s, "eval/child") {
				t.Fatal("refused rm removed rows")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+"-saved", path); err != nil {
				t.Fatal(err)
			}
			for _, path := range append(paths, outside) {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("refused rm changed %s: %v", path, err)
				}
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatalf("outside sentinel: %v", err)
			}
		})
	}
}

// Recursive removal unlinks nested symlinks without touching their targets.
func TestGroupRmDoesNotFollowNestedSymlink(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval")
	cfg := groupRmConfig(t)
	seedGroupRmPaths(t, cfg, []string{"eval"})
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.GroupsDir, "eval", "link")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, cfg, nil, "eval", true, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.GroupsDir, "eval")); !os.IsNotExist(err) {
		t.Fatalf("group directory remains: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("outside sentinel: %v", err)
	}
}

// A stop error keeps every directory after the rows are purged.
func TestGroupRmStopFailureKeepsFiles(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	cfg := groupRmConfig(t)
	paths := seedGroupRmPaths(t, cfg, []string{"eval", "eval/child"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/runs/stop", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	runed := httptest.NewServer(mux)
	defer runed.Close()
	client := runedv1.NewClient(runed.URL, "", time.Second)
	var out bytes.Buffer
	err := runGroupRm(context.Background(), s, cfg, client, "eval", false, &out)
	if err == nil || !strings.Contains(err.Error(), "rows removed, but:") {
		t.Fatalf("stop failure = %v", err)
	}
	if hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Error("stop failure restored rows")
	}
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(path, "content")); err != nil {
			t.Errorf("stop failure changed %s: %v", path, err)
		}
	}
}

// Groups with missing directory roots still purge successfully.
func TestGroupRmMissingDirectories(t *testing.T) {
	s := routdStore(t)
	seedGroups(t, s, "eval", "eval/child")
	cfg := groupRmConfig(t)
	var out bytes.Buffer
	if err := runGroupRm(context.Background(), s, cfg, nil, "eval", true, &out); err != nil {
		t.Fatal(err)
	}
	if hasGroup(s, "eval") || hasGroup(s, "eval/child") {
		t.Fatal("missing directories prevented row deletion")
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
