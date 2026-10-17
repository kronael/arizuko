package routd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

func TestGDriveFiles_RESTRoundTrip(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "eng"})
	h := NewServer(db, nil, nil, nil, 0, "").Handler()

	rec := doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "1AbC_d-e"})
	if rec.Code != 201 {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}
	var bound struct {
		FileID       string `json:"file_id"`
		OwnerFolder  string `json:"owner_folder"`
		TargetFolder string `json:"target_folder"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &bound)
	if bound.FileID != "1AbC_d-e" || bound.OwnerFolder != "eng" || bound.TargetFolder != "eng" {
		t.Fatalf("register body = %s", rec.Body.String())
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("seeded routes = %d, want 4", n)
	}

	rec = doJSON(t, h, "GET", "/v1/gdrive_files?owner_folder=eng", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"file_id":"1AbC_d-e"`) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "GET", "/v1/gdrive_files?owner_folder=other", "", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "1AbC_d-e") {
		t.Fatalf("list for another owner status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "1AbC_d-e"})
	if rec.Code != 409 {
		t.Fatalf("duplicate status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("a refused duplicate changed the routes: %d, want 4", n)
	}
	for _, id := range []string{"a/b", "a*b", "*", "a b"} {
		rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
			map[string]string{"owner_folder": "eng", "file_id": id})
		if rec.Code != 400 {
			t.Fatalf("file_id %q status=%d body=%s", id, rec.Code, rec.Body.String())
		}
	}
	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "", map[string]string{"owner_folder": "eng"})
	if rec.Code != 400 {
		t.Fatalf("missing file_id status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "x1", "target_folder": "eng/../ops"})
	if rec.Code != 400 {
		t.Fatalf("dot-dot target status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("a refused register seeded routes: %d, want 4", n)
	}

	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "x2", "target_folder": "ops"})
	if rec.Code != 403 {
		t.Fatalf("outside-subtree target status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "x3", "target_folder": "engineering"})
	if rec.Code != 403 {
		t.Fatalf("sibling sharing a prefix status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("a forbidden register seeded routes: %d, want 4", n)
	}
	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "x4", "target_folder": "eng/sre"})
	if rec.Code != 201 {
		t.Fatalf("descendant target status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "DELETE", "/v1/gdrive_files/x4?owner_folder=eng", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("revoke x4 status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, "DELETE", "/v1/gdrive_files/1AbC_d-e?owner_folder=other", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":false`) {
		t.Fatalf("cross-folder revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("a cross-folder revoke removed routes: %d, want 4", n)
	}
	rec = doJSON(t, h, "DELETE", "/v1/gdrive_files/1AbC_d-e?owner_folder=eng", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("routes left after revoke = %d", n)
	}
}

func TestGDriveFiles_RESTOwnerOutsideTokenFolderIs403(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "u", scope: []string{"routes:write"}, folder: "eng"})
	_ = db.PutGroup(core.Group{Folder: "eng"})
	_ = db.PutGroup(core.Group{Folder: "ops"})

	rec := doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "ops", "file_id": "f1"})
	if rec.Code != 403 {
		t.Fatalf("owner outside the token subtree status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("a forbidden register seeded routes: %d", n)
	}
	rec = doJSON(t, h, "POST", "/v1/gdrive_files", "",
		map[string]string{"owner_folder": "eng", "file_id": "f1"})
	if rec.Code != 201 {
		t.Fatalf("owner inside the token subtree status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "DELETE", "/v1/gdrive_files/f1?owner_folder=ops", "", nil)
	if rec.Code != 403 {
		t.Fatalf("revoke as an owner outside the token subtree status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func serveGDriveFilesMCP(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	callerSub := "folder:" + folder
	pb := srv.gdriveFilesPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder}),
		srv.turnVisible(turnMCP{folder: folder}))
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

func TestGDriveFilesMCP_RegisterListRevoke(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	grantMCPTools(t, db, "acme", "register_gdrive_file", "list_gdrive_files", "revoke_gdrive_file")
	sock := serveGDriveFilesMCP(t, db, "acme")

	text, e := callToolText(t, sock, "register_gdrive_file", map[string]any{"file_id": "f1", "target_folder": "acme/sub"})
	if e != "" || !strings.Contains(text, `"target_folder":"acme/sub"`) {
		t.Fatalf("register: text=%q err=%q", text, e)
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("seeded routes = %d, want 4", n)
	}
	if _, e := callToolText(t, sock, "register_gdrive_file", map[string]any{"file_id": "f1"}); e == "" {
		t.Fatal("a second register of the same file should be refused")
	}
	text, e = callToolText(t, sock, "list_gdrive_files", nil)
	if e != "" || !strings.Contains(text, `"file_id":"f1"`) {
		t.Fatalf("list: text=%q err=%q", text, e)
	}
	text, e = callToolText(t, sock, "revoke_gdrive_file", map[string]any{"file_id": "f1"})
	if e != "" || text != `{"deleted":true}` {
		t.Fatalf("revoke: text=%q err=%q", text, e)
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("routes left after revoke = %d", n)
	}
}

// The turn gate, not the handler, binds an agent's target_folder: the handler
// skips groupfolder.Contains on the MCP surface, so a gate that stopped asking
// would let an agent route a file into a sibling folder.
func TestGDriveFilesMCP_TargetOutsideTheGrantIsDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	grantMCPTools(t, db, "acme", "register_gdrive_file", "list_gdrive_files", "revoke_gdrive_file")
	sock := serveGDriveFilesMCP(t, db, "acme")

	for _, target := range []string{"other", "acme-two", "acme/../other"} {
		if _, e := callToolText(t, sock, "register_gdrive_file", map[string]any{"file_id": "f1", "target_folder": target}); e == "" {
			t.Fatalf("target %q should be denied", target)
		}
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("a denied register seeded routes: %d", n)
	}
	if rows, _ := db.store().ListGDriveFiles(""); len(rows) != 0 {
		t.Fatalf("a denied register wrote a binding: %+v", rows)
	}
}

func TestGDriveFilesMCP_UngrantedFolderSeesNoTool(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	grantMCPTools(t, db, "acme", "register_gdrive_file", "list_gdrive_files", "revoke_gdrive_file")

	granted := listToolNames(t, serveGDriveFilesMCP(t, db, "acme"))
	ungranted := listToolNames(t, serveGDriveFilesMCP(t, db, "other"))
	for _, name := range []string{"register_gdrive_file", "list_gdrive_files", "revoke_gdrive_file"} {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
		if ungranted[name] {
			t.Fatalf("%s visible to a folder never delegated it", name)
		}
	}
}

func TestServeTurnMCP_ElevatedRegistersGDriveFile(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "hq", turnID: "t1", elevated: true}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	t.Cleanup(stop)

	if text, e := callToolText(t, groupfolder.IpcSocket(ipcDir), "register_gdrive_file", map[string]any{"file_id": "f1"}); e != "" {
		t.Fatalf("register: text=%q err=%q", text, e)
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("seeded routes = %d, want 4", n)
	}
}

func TestGDriveFilesMCP_RevokeCrossFolderDeletesNothing(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	grantMCPTools(t, db, "acme", "register_gdrive_file")
	grantMCPTools(t, db, "other", "revoke_gdrive_file")
	acme := serveGDriveFilesMCP(t, db, "acme")
	other := serveGDriveFilesMCP(t, db, "other")

	if _, e := callToolText(t, acme, "register_gdrive_file", map[string]any{"file_id": "f1"}); e != "" {
		t.Fatalf("register: %s", e)
	}
	text, e := callToolText(t, other, "revoke_gdrive_file", map[string]any{"file_id": "f1"})
	if e != "" || text != `{"deleted":false}` {
		t.Fatalf("cross-folder revoke: text=%q err=%q", text, e)
	}
	if n := routeCount(t, db); n != 4 {
		t.Fatalf("a cross-folder revoke removed routes: %d, want 4", n)
	}
}
