package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedHeld inserts one held call for the REST tests, and the admin row on its
// folder that a verdict is authorized against (BUGS L271) for every reviewer
// these tests speak as.
func seedHeld(t *testing.T, db *DB, id, folder, chatJID string) {
	t.Helper()
	if err := db.PutPendingAction(PendingAction{
		ID: id, GroupFolder: folder, Tool: "send_file", ChatJID: chatJID,
		Args: `{"path":"/etc/x"}`,
	}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"op", "google:op@x"} {
		addACL(t, db, sub, "admin", folder, "allow")
	}
}

// triggerText returns (count, concatenated content) of non-bot rows in a chat.
func triggerText(t *testing.T, db *DB, jid string) (int, string) {
	t.Helper()
	var n int
	var all strings.Builder
	for _, m := range chatMessages(t, db, jid) {
		if m.BotMsg {
			continue
		}
		all.WriteString(m.Content)
		n++
	}
	return n, all.String()
}

// TestPendingActionsListFiltersStatus: the operator list face returns held rows
// and narrows on ?status=.
func TestPendingActionsListFiltersStatus(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "tg:9")
	seedHeld(t, db, "p2", "demo", "tg:9")
	if _, err := resolvePending(db, "p2", PendingRejected, "op", ""); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/pending_actions?status=held", nil))
	if rec.Code != 200 {
		t.Fatalf("list = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Pending []PendingAction `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Pending) != 1 || out.Pending[0].ID != "p1" {
		t.Fatalf("held list = %+v, want exactly p1", out.Pending)
	}
}

// TestPendingActionsApprove: the REST verdict runs the same funnel as chat
// /approve — the row moves and the reviewer from the body is recorded. It tells
// nobody to run anything: the agent is suspended on the row, so the verdict
// reaching it IS the delivery.
func TestPendingActionsApprove(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "tg:9")

	rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{"note": "fine", "reviewer": "google:op@x"})
	if rec.Code != 200 {
		t.Fatalf("approve = %d body=%s", rec.Code, rec.Body.String())
	}
	row, ok := pendingActionRow(db, "p1")
	if !ok || row.Status != PendingApproved {
		t.Fatalf("row = %+v, want approved", row)
	}
	if row.ReviewedBy != "google:op@x" || row.ReviewerNote != "fine" {
		t.Fatalf("reviewer not recorded: %+v", row)
	}
	if n, content := triggerText(t, db, "tg:9"); n != 0 {
		t.Fatalf("the verdict wrote %d message(s) into the held chat: %s", n, content)
	}
}

// TestPendingActionsApproveChatless: a call held outside any chat — the delegate
// shape — is answerable exactly like any other, and the verdict still wakes
// nobody.
func TestPendingActionsApproveChatless(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "")

	if rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{"reviewer": "op"}); rec.Code != 200 {
		t.Fatalf("approve = %d body=%s", rec.Code, rec.Body.String())
	}
	row, _ := pendingActionRow(db, "p1")
	if row.Status != PendingApproved {
		t.Fatalf("status = %q, want approved", row.Status)
	}
	if n, _ := triggerText(t, db, "demo"); n != 0 {
		t.Fatalf("the verdict wrote %d message(s) onto the folder JID, want none", n)
	}
}

// TestPendingActionsRejectWritesNoTrigger: a rejected call is never run, so no
// resolution message may appear anywhere.
func TestPendingActionsRejectWritesNoTrigger(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "tg:9")

	rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/reject", "",
		map[string]string{"note": "nope", "reviewer": "op"})
	if rec.Code != 200 {
		t.Fatalf("reject = %d body=%s", rec.Code, rec.Body.String())
	}
	row, _ := pendingActionRow(db, "p1")
	if row.Status != PendingRejected {
		t.Fatalf("status = %q, want rejected", row.Status)
	}
	if n, _ := triggerText(t, db, "tg:9"); n != 0 {
		t.Fatal("reject wrote a resolution message")
	}
}

// TestPendingActionsVerdictErrors: unknown id → 404; second verdict → 409 — the
// double-approve race surfaces as a conflict, not a silent success.
func TestPendingActionsVerdictErrors(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "tg:9")

	if rec := doJSON(t, h, "POST", "/v1/pending_actions/nope/approve", "",
		map[string]string{}); rec.Code != 404 {
		t.Fatalf("unknown id = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{"reviewer": "op"}); rec.Code != 200 {
		t.Fatalf("first approve = %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/reject", "",
		map[string]string{"reviewer": "op"}); rec.Code != 409 {
		t.Fatalf("second verdict = %d, want 409", rec.Code)
	}
	if n, _ := triggerText(t, db, "tg:9"); n != 0 {
		t.Fatal("a verdict wrote a message into the held chat")
	}
}

// TestPendingActionsScopeGate: the face demands pending_actions:read for the
// list and pending_actions:write for a verdict — a read-scoped bearer can look
// but not resolve.
func TestPendingActionsScopeGate(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:x", scope: []string{"messages:write"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/pending_actions", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list without scope = %d, want 403", rec.Code)
	}

	db2, h2 := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"pending_actions:read"}})
	seedHeld(t, db2, "p1", "demo", "tg:9")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest("GET", "/v1/pending_actions", nil))
	if rec2.Code != 200 {
		t.Fatalf("list with read scope = %d body=%s", rec2.Code, rec2.Body.String())
	}
	if rec3 := doJSON(t, h2, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{}); rec3.Code != http.StatusForbidden {
		t.Fatalf("approve with read scope = %d, want 403", rec3.Code)
	}
}
