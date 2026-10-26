package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kronael/arizuko/container"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

const stubRunedToken = "dashd-service-token"

// fakeRuned serves the two runed routes the delete page uses. onStop sees each
// stopped folder at request time, so a test can read the store while runed
// holds the request.
type fakeRuned struct {
	url string
	srv *httptest.Server

	mu      sync.Mutex
	stopped []string
	bearers []string
}

func stubRuned(t *testing.T, stopStatus int, onStop func(folder string)) *fakeRuned {
	t.Helper()
	f := &fakeRuned{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /v1/runs/stop", func(w http.ResponseWriter, r *http.Request) {
		var req runedv1.StopRunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.stopped = append(f.stopped, req.Folder)
		f.bearers = append(f.bearers, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if onStop != nil {
			onStop(req.Folder)
		}
		if stopStatus != http.StatusOK {
			http.Error(w, `{"error":"boom"}`, stopStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(runedv1.StopRunResponse{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	f.srv = srv
	return f
}

func withRuned(d *dash, f *fakeRuned) {
	d.runedURL = f.url
	d.svc = func(context.Context) (string, error) { return stubRunedToken, nil }
}

func deleteTeam(t *testing.T, d *dash) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	return w
}

// The real delete handler withdraws nested selections before stopping purged groups.
func TestGroupDeletePackages(t *testing.T) {
	d, driver := splitAdminDash(t, "alice@x")
	seedFolder(t, driver, "team/child")
	seedFolder(t, driver, "keeper")
	root := filepath.Dir(d.groupsDir)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "arizuko.toml"), []byte("name = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := container.LockPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := container.PreparePackage(root, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"team", "team/child", "keeper", ""} {
		if err := container.SelectPackages(root, folder, []container.PackageSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			if err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.IsDir() {
					return err
				}
				return os.Chmod(path, 0o755)
			}); err != nil {
				t.Error(err)
			}
		}
	})
	withRuned(d, stubRuned(t, http.StatusOK, func(folder string) {
		selected, err := container.ReadPackageSelection(root, folder)
		if err != nil || len(selected) != 0 {
			t.Errorf("stop saw active packages for %s: %v %v", folder, selected, err)
		}
		if hasFolder(t, driver, folder) {
			t.Errorf("stop saw registered folder %s", folder)
		}
	}))
	if w := deleteTeam(t, d); w.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	for _, folder := range []string{"team", "team/child", "keeper", ""} {
		selected, err := container.ReadPackageSelection(root, folder)
		want := 0
		if folder == "keeper" || folder == "" {
			want = 1
		}
		if err != nil || len(selected) != want {
			t.Fatalf("selection %s = %v %v", folder, selected, err)
		}
	}
}

func TestGroupDeletePendingAdoption(t *testing.T) {
	for _, pending := range []string{"core", "group", "malformed", "instance"} {
		t.Run(pending, func(t *testing.T) {
			d, driver := splitAdminDash(t, "alice@x")
			seedFolder(t, driver, "team/child")
			seedFolder(t, driver, "keeper")
			root := filepath.Dir(d.groupsDir)
			row := store.InstalledPackage{Folder: "team/child", Name: "waiting", Source: "origin", Revision: "revision",
				AssetHashes: map[string]string{"pending:adopt:source": strings.Repeat("a", 64),
					"pending:adopt:package_source":                              strings.Repeat("b", 64),
					"pending:adopt:tree:groups/team/child/.claude/skills/probe": strings.Repeat("c", 64),
					container.PendingSettingPrefix + "KEEP":                     strings.Repeat("d", 64)}}
			switch pending {
			case "core":
				row.Folder, row.Name = "", "core"
			case "malformed":
				row.Folder, row.AssetHashes = "keeper", map[string]string{"pending:adopt:unknown": "invalid"}
			case "instance":
				row.Folder = "keeper"
				row.AssetHashes["pending:adopt:tree:skills/probe"] = strings.Repeat("e", 64)
			}
			if err := d.routdStore().PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			for _, folder := range []string{"team", "team/child", "keeper"} {
				path := filepath.Join(root, "groups", folder, "content")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(folder), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runed := stubRuned(t, http.StatusOK, nil)
			withRuned(d, runed)
			if w := deleteTeam(t, d); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "pending package adoption") {
				t.Fatalf("delete bypassed adoption: %d %s", w.Code, w.Body.String())
			}
			if len(runed.stopped) != 0 {
				t.Fatalf("refusal stopped runs: %v", runed.stopped)
			}
			for _, folder := range []string{"team", "team/child", "keeper"} {
				if !hasFolder(t, driver, folder) {
					t.Fatalf("refusal purged %s", folder)
				}
				if data, err := os.ReadFile(filepath.Join(root, "groups", folder, "content")); err != nil || string(data) != folder {
					t.Fatalf("refusal removed files: %s %v", data, err)
				}
			}
			got, found, err := d.routdStore().InstalledPackage(row.Folder, row.Name)
			if err != nil || !found || !reflect.DeepEqual(got.AssetHashes, row.AssetHashes) {
				t.Fatalf("refusal changed adoption/settings: %+v, %v", got, err)
			}
		})
	}
}

// Deleting `team` with child `team/child` stops runed's run in each folder with
// dashd's service token, and only after the folder's rows are gone: a folder
// that still resolves when it is stopped can be re-dispatched by routd.
func TestGroupDeleteStopsEachFolderAfterThePurge(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolder(t, routd, "team/child")
	seedFolder(t, routd, "keeper")
	stillResolved := map[string]bool{}
	var mu sync.Mutex
	runed := stubRuned(t, http.StatusOK, func(folder string) {
		mu.Lock()
		defer mu.Unlock()
		_, stillResolved[folder] = store.NewOver(routd).GroupByFolder(folder)
	})
	withRuned(d, runed)

	w := deleteTeam(t, d)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}
	slices.Sort(runed.stopped)
	if !slices.Equal(runed.stopped, []string{"team", "team/child"}) {
		t.Fatalf("stopped = %v, want [team team/child]", runed.stopped)
	}
	for _, folder := range runed.stopped {
		if stillResolved[folder] {
			t.Errorf("%s still had a groups row when its run was stopped", folder)
		}
	}
	for _, got := range runed.bearers {
		if got != "Bearer "+stubRunedToken {
			t.Errorf("stop sent Authorization %q, want dashd's service token", got)
		}
	}
	if !hasFolder(t, routd, "keeper") {
		t.Error("the delete reached the sibling folder keeper")
	}
}

// runed answers 500 to the stop: the purge stands, so the page answers 200 and
// tells the operator that a run may still be live. A 500 here would tell them
// the rows are still there.
func TestGroupDeleteStopFailureAnswers200WithWarning(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	withRuned(d, stubRuned(t, http.StatusInternalServerError, nil))

	w := deleteTeam(t, d)

	if w.Code != http.StatusOK {
		t.Fatalf("POST delete = %d, want 200 body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `banner-warn`) || !strings.Contains(body, "stop team") {
		t.Errorf("page lacks the warning that names the folder: %s", body)
	}
	if hasFolder(t, routd, "team") {
		t.Error("the stop failure undid the purge")
	}
}

// runed does not answer /health: nothing is removed, because a live run could
// outlive the group, and the page says why and names the escape.
func TestGroupDeleteRefusesWhenRunedIsUnreachable(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	dead := stubRuned(t, http.StatusOK, nil)
	withRuned(d, dead)
	dead.srv.Close()

	w := deleteTeam(t, d)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST delete = %d, want 503 body=%q", w.Code, w.Body.String())
	}
	if !hasFolder(t, routd, "team") {
		t.Error("the group was removed although runed was unreachable")
	}
	body := w.Body.String()
	if !strings.Contains(body, dead.url) || !strings.Contains(body, "rm -f team") {
		t.Errorf("page does not say why or how to proceed: %q", body)
	}
}
