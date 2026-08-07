package routd

// Tests for the spec 5/28 installed_packages resreg registration: one handler,
// two faces, one evaluator. The surface is deliberately READ-ONLY (list + get) —
// see packages_resource.go — so what these prove is that both faces reach the
// same record and that the instance-wide containment holds on each.
//
// The table has NO folder column: a package installs instance-wide and its
// record names cross-folder identities (acl grants applied, public route paths
// opened). So "containment" here is not "see only your rows" — there is no
// per-folder slice — it is "a folder-scoped caller sees NOTHING". Each gate test
// therefore grants the caller the exact action it needs at its OWN subtree and
// still expects a denial: only the `**` scope contains an instance-wide record.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// ttsdRecord is the seeded installed-package row every test below reads back.
// It carries a manifest + asset hashes so the JSON-column decode is exercised,
// not just the flat columns.
var ttsdRecord = InstalledPackage{
	Name:     "ttsd",
	Source:   "github.com/kronael/ttsd",
	Revision: "9f1c2ab",
	Manifest: map[string][]string{
		"compose_fragment": {"ttsd.yml", "ttsd-routes.json"},
		"proxyd_route":     {"/tts/"},
	},
	AssetHashes: map[string]string{"file:ttsd.yml": "deadbeef"},
	InstalledAt: "2026-08-05T10:00:00Z",
}

// packagesFixture opens a routd DB holding one group and one installed package.
func packagesFixture(t *testing.T) *DB {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "hq"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutInstalledPackage(ttsdRecord); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return db
}

// servePackagesMCP stands up the agent socket for folder + the installed_packages
// resreg seam and returns the socket path. Authz reads the folder's acl rows.
func servePackagesMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.installedPackagesPostBuild(folder, callerSub, srv.db.Authorize,
		agentVisibleFor(srv, callerSub, false))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// grantAt adds one acl allow row for principal at an explicit scope — the knob
// the containment tests turn (own subtree vs the whole tree).
func grantAt(t *testing.T, db *DB, principal, action, scope string) {
	t.Helper()
	if err := db.AddACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: "allow",
	}); err != nil {
		t.Fatalf("grant %s@%s to %s: %v", action, scope, principal, err)
	}
}

// TestInstalledPackagesMCP_ListAndGet: the agent face reaches the record when the
// caller holds the tools at `**`. Both tools render the SAME struct the REST face
// and /openapi.json use, so the manifest/asset_hashes maps must survive the trip.
func TestInstalledPackagesMCP_ListAndGet(t *testing.T) {
	db := packagesFixture(t)
	grantAt(t, db, "folder:hq", "mcp:list_packages", "**")
	grantAt(t, db, "folder:hq", "mcp:get_package", "**")
	sock := servePackagesMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "list_packages", nil)
	if e != "" {
		t.Fatalf("list_packages errored: %s", e)
	}
	var listed []struct {
		Name        string              `json:"name"`
		Source      string              `json:"source"`
		Revision    string              `json:"revision"`
		Manifest    map[string][]string `json:"manifest"`
		AssetHashes map[string]string   `json:"asset_hashes"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatalf("list payload %q not JSON: %v", text, err)
	}
	if len(listed) != 1 || listed[0].Name != "ttsd" || listed[0].Revision != "9f1c2ab" {
		t.Fatalf("list_packages = %s, want the one ttsd@9f1c2ab row", text)
	}
	if got := listed[0].Manifest["proxyd_route"]; len(got) != 1 || got[0] != "/tts/" {
		t.Fatalf("manifest proxyd_route = %v, want [/tts/]", got)
	}
	if listed[0].AssetHashes["file:ttsd.yml"] != "deadbeef" {
		t.Fatalf("asset_hashes = %v, want file:ttsd.yml=deadbeef", listed[0].AssetHashes)
	}

	text, e = callToolText(t, sock, "get_package", map[string]any{"name": "ttsd"})
	if e != "" {
		t.Fatalf("get_package errored: %s", e)
	}
	var one struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		t.Fatalf("get payload %q not JSON: %v", text, err)
	}
	if one.Name != "ttsd" || one.Source != "github.com/kronael/ttsd" {
		t.Fatalf("get_package = %s, want the ttsd row", text)
	}

	// An absent package is 404, not an empty row that reads as "installed".
	if _, e := callToolText(t, sock, "get_package", map[string]any{"name": "nope"}); e == "" {
		t.Fatal("get_package on an uninstalled name succeeded")
	}
}

// TestInstalledPackagesMCP_OwnSubtreeGrantDenied is the agent-face containment
// proof. The caller holds mcp:list_packages — but scoped to its OWN folder, which
// is every bit of authority a tenant agent can legitimately be delegated. The
// record is instance-wide, so `hq/**` does not contain it and the call is denied.
// Falsifiable: point installedPackagesScope at the caller's folder and this test
// fails while every other test in the file still passes.
func TestInstalledPackagesMCP_OwnSubtreeGrantDenied(t *testing.T) {
	db := packagesFixture(t)
	grantAt(t, db, "folder:hq", "mcp:list_packages", "hq/**")
	grantAt(t, db, "folder:hq", "mcp:get_package", "hq/**")
	sock := servePackagesMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "list_packages", nil)
	if e == "" {
		t.Fatalf("a folder-scoped grant read instance-wide package state: %s", text)
	}
	if strings.Contains(text+e, "ttsd") {
		t.Fatalf("denial leaked the record: %s / %s", text, e)
	}
	if text, e := callToolText(t, sock, "get_package", map[string]any{"name": "ttsd"}); e == "" {
		t.Fatalf("a folder-scoped grant read one package's record: %s", text)
	}
}

// TestInstalledPackagesMCP_ToolsListGating: neither tool is in the role:member
// floor, so an ungranted folder is not even shown them; a folder granted them at
// `**` is. Visibility and authorization are separate views (5/17) — this pins the
// visibility half, the denial tests above pin the other.
func TestInstalledPackagesMCP_ToolsListGating(t *testing.T) {
	db := packagesFixture(t)
	bare := listToolNames(t, servePackagesMCP(t, db, "hq", "folder:hq"))
	for _, tl := range []string{"list_packages", "get_package"} {
		if bare[tl] {
			t.Errorf("%s advertised to a folder holding no grant for it", tl)
		}
	}

	grantAt(t, db, "folder:hq", "mcp:list_packages", "**")
	grantAt(t, db, "folder:hq", "mcp:get_package", "**")
	granted := listToolNames(t, servePackagesMCP(t, db, "hq", "folder:hq"))
	for _, tl := range []string{"list_packages", "get_package"} {
		if !granted[tl] {
			t.Errorf("%s missing from tools/list for a granted folder", tl)
		}
	}
}

// packagesREST returns a GET helper against routd's REAL mux (Server.Handler),
// behind a Verifier reporting sub — so it also proves server.go mounts the
// resource, not just that the resource could be mounted.
func packagesREST(t *testing.T, db *DB, sub string) func(path string) (int, string) {
	t.Helper()
	verify := verifierFunc(func(*http.Request) (string, []string, string, error) {
		return sub, nil, "", nil
	})
	h := NewServer(db, nil, nil, verify, 0, "").Handler()
	return func(path string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code, w.Body.String()
	}
}

// TestInstalledPackagesREST_OperatorReads: the operator (role:operator, `*` at
// `**`) reads both endpoints through the SAME handler the agent tools use.
func TestInstalledPackagesREST_OperatorReads(t *testing.T) {
	db := packagesFixture(t)
	if err := db.AddMembership("user:op", "role:operator", "seed"); err != nil {
		t.Fatal(err)
	}
	get := packagesREST(t, db, "user:op")

	code, body := get("/v1/installed_packages")
	if code != http.StatusOK {
		t.Fatalf("operator list = %d, want 200 (%s)", code, body)
	}
	if !strings.Contains(body, `"name":"ttsd"`) || !strings.Contains(body, `"revision":"9f1c2ab"`) {
		t.Fatalf("operator list body missing the record: %s", body)
	}

	code, body = get("/v1/installed_packages/ttsd")
	if code != http.StatusOK {
		t.Fatalf("operator get = %d, want 200 (%s)", code, body)
	}
	if !strings.Contains(body, `"source":"github.com/kronael/ttsd"`) {
		t.Fatalf("operator get body missing the record: %s", body)
	}

	if code, _ := get("/v1/installed_packages/nope"); code != http.StatusNotFound {
		t.Fatalf("get of an uninstalled package = %d, want 404", code)
	}
}

// TestInstalledPackagesREST_OwnSubtreeGrantDenied is the REST-face twin of the
// agent containment proof: a tenant holding the EXACT action, scoped to its own
// subtree, is denied — and the 403 body carries none of the record. Same
// evaluator, same target, different identity source.
func TestInstalledPackagesREST_OwnSubtreeGrantDenied(t *testing.T) {
	db := packagesFixture(t)
	grantAt(t, db, "user:tenant", "installed_packages:list", "hq/**")
	grantAt(t, db, "user:tenant", "installed_packages:get", "hq/**")
	get := packagesREST(t, db, "user:tenant")

	code, body := get("/v1/installed_packages")
	if code != http.StatusForbidden {
		t.Fatalf("folder-scoped tenant list = %d, want 403 (%s)", code, body)
	}
	if strings.Contains(body, "ttsd") {
		t.Fatalf("403 body leaked the record: %s", body)
	}
	if code, body := get("/v1/installed_packages/ttsd"); code != http.StatusForbidden {
		t.Fatalf("folder-scoped tenant get = %d, want 403 (%s)", code, body)
	}
}

// TestInstalledPackagesInRoutdOpenAPI: routd serves the resource over REST, so
// routd's /openapi.json must advertise it. Mounted-but-undocumented is how a
// resource stays invisible to every operator tool.
func TestInstalledPackagesInRoutdOpenAPI(t *testing.T) {
	b := routdDoc(t)
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("doc not JSON: %v", err)
	}
	for _, p := range []string{"/v1/installed_packages", "/v1/installed_packages/{name}"} {
		if doc.Paths[p]["get"] == nil {
			t.Errorf("routd's openapi.json does not document GET %s", p)
		}
	}
}

// TestInstalledPackagesREST_NoWriteFace pins the honest surface: the resource
// declares only two GETs, so every write verb 404s at the mux. If somebody later
// adds a create/delete Endpoint without building the CLI's install pipeline
// behind it, this test is what fails.
func TestInstalledPackagesREST_NoWriteFace(t *testing.T) {
	db := packagesFixture(t)
	if err := db.AddMembership("user:op", "role:operator", "seed"); err != nil {
		t.Fatal(err)
	}
	verify := verifierFunc(func(*http.Request) (string, []string, string, error) {
		return "user:op", nil, "", nil
	})
	h := NewServer(db, nil, nil, verify, 0, "").Handler()

	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/installed_packages"},
		{"PATCH", "/v1/installed_packages/ttsd"},
		{"PUT", "/v1/installed_packages/ttsd"},
		{"DELETE", "/v1/installed_packages/ttsd"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want no write face (404/405)", tc.method, tc.path, w.Code)
		}
	}
	// The record survived every attempt.
	if _, ok, err := db.InstalledPackage(InstanceWide, "ttsd"); err != nil || !ok {
		t.Fatalf("record gone after write attempts: ok=%v err=%v", ok, err)
	}
}
