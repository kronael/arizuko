package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type coreReceiptFailure struct {
	objstore.Store
}

func (database coreReceiptFailure) GuardedBatch(ctx context.Context,
	checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Entity == store.InstalledPackages {
			return false, errors.New("receipt write refused")
		}
	}
	return database.Store.GuardedBatch(ctx, checks, writes)
}

func groupCoreSource(t *testing.T) {
	t.Helper()
	release := t.TempDir()
	for name, body := range map[string]string{
		"arizuko.toml": "name = \"core\"\n", "CLAUDE.md": "core manual",
		"skills/base/SKILL.md": "core skill", "output-styles/brief.md": "core style",
	} {
		path := filepath.Join(release, "ant", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("APP_SRC_DIR", release)
	dataDir := os.Getenv("DATA_DIR")
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			root := filepath.Join(dataDir, name)
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.IsDir() {
					return err
				}
				return os.Chmod(path, 0o755)
			})
		}
	})
}

func TestGroupCreatePageCoreFailure(t *testing.T) {
	for _, failure := range []string{"source", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			d, db := splitAdminDash(t, "alice@x")
			t.Setenv("DATA_DIR", t.TempDir())
			groupCoreSource(t)
			if failure == "source" {
				t.Setenv("APP_SRC_DIR", t.TempDir())
			} else {
				d.routd = coreReceiptFailure{d.routd}
			}
			w := httptest.NewRecorder()
			newMux(d).ServeHTTP(w, adminReq("POST", "/dash/groups/new", "folder=broken", "alice@x"))
			if w.Code != http.StatusInternalServerError || hasFolder(t, db, "broken") {
				t.Fatalf("core failure published a group: %d %s", w.Code, w.Body.String())
			}
			if failure == "receipt" {
				selected, err := container.ReadPackageSelection(os.Getenv("DATA_DIR"), "")
				if err != nil || len(selected) != 1 {
					t.Fatalf("receipt failure did not reach activation: %+v %v", selected, err)
				}
			}
		})
	}
}

// TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder is BUGS L247 stated as
// an assertion. POST /dash/groups/new writes the group row, the member edge,
// the creator's admin grant and the audit row in ONE transaction, and
// `specs/6/37` § "How a storage layer is organized" rule 3 names that shape
// security-bearing: the escalation it prevents is a folder whose creator holds
// no admin on it, or holds one the audit does not record.
//
// It drives the HANDLER, not CreateGroupWithAdmin. A fixture that called the
// store method would prove the method and leave the page — the part that
// resolves the caller and decides the admin principal — untested, which is the
// hole the entry names.
func TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	t.Setenv("DATA_DIR", t.TempDir())
	groupCoreSource(t)

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/new", "folder=team2", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST create = %d body=%q, want 303", w.Code, w.Body.String())
	}

	if !hasFolder(t, routd, "team2") {
		t.Fatal("the group row is missing, so the folder was never created")
	}
	selected, err := container.ReadPackageSelection(os.Getenv("DATA_DIR"), "")
	receipt, coreFound, rerr := store.NewOver(routd).InstalledPackage("", "core")
	if err != nil || rerr != nil || len(selected) != 1 || !coreFound ||
		receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
		t.Fatalf("core missing before create success: %+v %+v %v %v", selected, receipt, err, rerr)
	}
	// The grant is read back as a ROW. auth.Authorize alone cannot see it here:
	// the creator already holds `**`, so the evaluator answers yes whether or
	// not this transaction wrote anything — a mutation that scoped the grant to
	// the wrong folder passed an Authorize-only assertion.
	rows, err := store.NewOver(routd).ListACL("alice@x")
	if err != nil {
		t.Fatalf("read back the creator's grants: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "admin" && row.Scope == "team2/**" && row.Effect == "allow" {
			found = true
		}
	}
	if !found {
		t.Errorf("no `admin` grant on team2/** for the creator; got %+v", rows)
	}
	// And the evaluator honours it, on the folder AND on a child of it. A bare
	// `team2` scope reads as admin of the folder and 403 on every child, because
	// matchSegments ends on len(in) == 0 (BUGS L206).
	creator := auth.Caller{Principal: "alice@x"}
	if !auth.Authorize(store.NewOver(routd), creator, "admin", "team2", nil) {
		t.Error("the creator holds no admin on the folder they just created")
	}
	if !auth.Authorize(store.NewOver(routd), creator, "admin", "team2/sre", nil) {
		t.Error("the creator holds no admin on a CHILD of the folder they just created")
	}
	if got := auditActor(t, d, "group.create"); got != "alice@x" {
		t.Errorf("group.create actor_sub = %q, want alice@x", got)
	}
}

// A folder the caller may not administer is refused BEFORE anything is written.
// requireAdmin runs after the folder syntax checks, so a partial write here
// would leave a group nobody was allowed to make.
func TestGroupCreatePage_RefusesAFolderTheCallerCannotAdminister(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	t.Setenv("DATA_DIR", t.TempDir())
	seedAdmin(t, routd, "bob@x", "bobland")

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/new", "folder=elsewhere", "bob@x"))
	if w.Code == http.StatusSeeOther {
		t.Fatalf("create succeeded for a folder bob may not administer")
	}
	if hasFolder(t, routd, "elsewhere") {
		t.Error("the refusal still wrote the group row")
	}
}
