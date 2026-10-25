package routd

import (
	"strings"
	"testing"
)

func TestACLRESTRejectsBareToolAction(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})
	rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:bob", Scope: "team", Action: "send",
	})
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "action") {
		t.Errorf("bare tool accepted: %d %s", rec.Code, rec.Body)
	}
	if rows, err := db.ListACL("google:bob"); err != nil || len(rows) != 0 {
		t.Fatalf("refused action persisted: %+v, %v", rows, err)
	}
}

func TestACLMCPRejectsBareToolAction(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sock := serveACLMCPElevated(t, db, "root")
	_, toolErr := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root", "action": "send",
	})
	if toolErr == "" || !strings.Contains(toolErr, "action") {
		t.Errorf("bare tool accepted: %s", toolErr)
	}
	if rows, err := db.ListACL("google:bob"); err != nil || len(rows) != 0 {
		t.Fatalf("refused action persisted: %+v, %v", rows, err)
	}
}
