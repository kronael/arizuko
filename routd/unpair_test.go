package routd

// Unpair (spec 5/31 § Unpair): one action on acl_membership, scoped to
// added_by='pairing'. Two faces, two endpoint proofs — the agent handling the
// chat (MCP) and the account holder (REST) — and neither can reach a role
// membership row.

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
	"github.com/kronael/arizuko/store"
)

// serveMembershipMCP stands up the agent socket with the unpair seam mounted.
func serveMembershipMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.membershipPostBuild(folder, callerSub, srv.db.Authorize,
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

// unpairFixture registers folder, routes `room=user/42` to it, writes a pairing
// edge plus a role membership on the same JID, and delegates unpair.
func unpairFixture(t *testing.T) *DB {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "hq"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Match: "room=user/42", Target: "hq"}); err != nil {
		t.Fatal(err)
	}
	st := store.New(db.SQL())
	if err := st.PutMembership("telegram:user/42", "google:alice", store.PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMembership("telegram:user/42", "role:member", "seed"); err != nil {
		t.Fatal(err)
	}
	grantMCPTools(t, db, "hq", "unpair")
	return db
}

func edgeExists(t *testing.T, db *DB, child, parent string) bool {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child = ? AND parent = ?`, child, parent,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// The agent handling the chat may unpair it; the role membership on the same
// JID is untouched.
func TestUnpairMCP_ChannelSide(t *testing.T) {
	db := unpairFixture(t)
	sock := serveMembershipMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "unpair",
		map[string]any{"child": "telegram:user/42", "parent": "google:alice"})
	if e != "" {
		t.Fatalf("unpair errored: %s", e)
	}
	var out struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || !out.Removed {
		t.Fatalf("unpair result %q", text)
	}
	if edgeExists(t, db, "telegram:user/42", "google:alice") {
		t.Error("pairing edge survived unpair")
	}
	if !edgeExists(t, db, "telegram:user/42", "role:member") {
		t.Error("unpair removed the role membership")
	}
}

// added_by='pairing' is the whole scope: a role membership cannot be deleted
// even when named explicitly.
func TestUnpairMCP_CannotReachRoleMembership(t *testing.T) {
	db := unpairFixture(t)
	sock := serveMembershipMCP(t, db, "hq", "folder:hq")

	_, e := callToolText(t, sock, "unpair",
		map[string]any{"child": "telegram:user/42", "parent": "role:member"})
	if e == "" {
		t.Fatal("unpair deleted a role membership")
	}
	if !strings.Contains(e, "no pairing") {
		t.Errorf("error = %q, want a not-found refusal", e)
	}
	if !edgeExists(t, db, "telegram:user/42", "role:member") {
		t.Fatal("role membership was deleted")
	}
}

// A folder that does not handle the chat cannot unpair it.
func TestUnpairMCP_ForeignChatRefused(t *testing.T) {
	db := unpairFixture(t)
	if err := db.PutGroup(core.Group{Folder: "rival"}); err != nil {
		t.Fatal(err)
	}
	grantMCPTools(t, db, "rival", "unpair")
	sock := serveMembershipMCP(t, db, "rival", "folder:rival")

	_, e := callToolText(t, sock, "unpair",
		map[string]any{"child": "telegram:user/42", "parent": "google:alice"})
	if e == "" {
		t.Fatal("a folder unpaired another folder's chat")
	}
	if !edgeExists(t, db, "telegram:user/42", "google:alice") {
		t.Fatal("the refused call still deleted the edge")
	}
}

// REST: the account holder may drop their own pairing. Verifier nil (local dev)
// leaves the face open, so drive the handler with a stubbed caller sub instead.
func TestUnpairREST_AccountSide(t *testing.T) {
	db := unpairFixture(t)
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")
	mux := http.NewServeMux()
	srv.mountMembership(mux)

	body := strings.NewReader(`{"child":"telegram:user/42","parent":"google:alice"}`)
	req := httptest.NewRequest("DELETE", "/v1/acl_membership", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status %d: %s", w.Code, w.Body.String())
	}
	if edgeExists(t, db, "telegram:user/42", "google:alice") {
		t.Error("REST unpair did not remove the edge")
	}
	if !edgeExists(t, db, "telegram:user/42", "role:member") {
		t.Error("REST unpair removed the role membership")
	}
}

// REST cannot reach a role membership either.
func TestUnpairREST_CannotReachRoleMembership(t *testing.T) {
	db := unpairFixture(t)
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")
	mux := http.NewServeMux()
	srv.mountMembership(mux)

	body := strings.NewReader(`{"child":"telegram:user/42","parent":"role:member"}`)
	req := httptest.NewRequest("DELETE", "/v1/acl_membership", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("DELETE role membership: status %d, want 404; %s", w.Code, w.Body.String())
	}
	if !edgeExists(t, db, "telegram:user/42", "role:member") {
		t.Fatal("role membership was deleted over REST")
	}
}

// An edge that pairing did not write (a manifest-applied membership) is out of
// reach from both faces.
func TestUnpair_LeavesManifestEdgesAlone(t *testing.T) {
	db := unpairFixture(t)
	st := store.New(db.SQL())
	if err := st.PutMembership("telegram:user/42", "google:applied", "manifest"); err != nil {
		t.Fatal(err)
	}
	sock := serveMembershipMCP(t, db, "hq", "folder:hq")

	if _, e := callToolText(t, sock, "unpair",
		map[string]any{"child": "telegram:user/42", "parent": "google:applied"}); e == "" {
		t.Fatal("unpair deleted a manifest-applied edge")
	}
	if !edgeExists(t, db, "telegram:user/42", "google:applied") {
		t.Fatal("manifest-applied edge was deleted")
	}
}

// With a live Verifier the REST face binds to the account: only the edge's
// parent may drop it.
func TestUnpairREST_OnlyTheParent(t *testing.T) {
	db := unpairFixture(t)
	var asSub string
	verify := verifierFunc(func(*http.Request) (string, []string, string, error) {
		return asSub, nil, "", nil
	})
	srv := NewServer(db, nil, nil, verify, 0, "https://x.test")
	mux := http.NewServeMux()
	srv.mountMembership(mux)

	del := func(child, parent string) int {
		body := strings.NewReader(`{"child":"` + child + `","parent":"` + parent + `"}`)
		req := httptest.NewRequest("DELETE", "/v1/acl_membership", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	asSub = "google:mallory"
	if code := del("telegram:user/42", "google:alice"); code != http.StatusForbidden {
		t.Fatalf("a stranger unpaired someone else's link: status %d", code)
	}
	if !edgeExists(t, db, "telegram:user/42", "google:alice") {
		t.Fatal("the refused call still deleted the edge")
	}

	asSub = "google:alice"
	if code := del("telegram:user/42", "google:alice"); code != http.StatusOK {
		t.Fatalf("the parent could not unpair its own link: status %d", code)
	}
	if edgeExists(t, db, "telegram:user/42", "google:alice") {
		t.Error("edge survived the parent's unpair")
	}
}
