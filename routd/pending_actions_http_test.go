package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedHeld inserts one held call for the REST tests.
func seedHeld(t *testing.T, db *DB, id, folder, chatJID string) {
	t.Helper()
	if err := db.PutPendingAction(PendingAction{
		ID: id, GroupFolder: folder, Tool: "send_file", ChatJID: chatJID,
		ArgsHash: "h", Args: `{"path":"/etc/x"}`, ArgsFinal: `{"path":"/etc/x"}`,
	}); err != nil {
		t.Fatal(err)
	}
}

// chatMessages returns (count, concatenated content) of non-bot rows in a chat.
func chatMessages(t *testing.T, db *DB, jid string) (int, string) {
	t.Helper()
	rows, err := db.SQL().Query(
		`SELECT content FROM messages WHERE chat_jid=? AND is_bot_message=0`, jid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var n int
	var all strings.Builder
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		all.WriteString(c)
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
	if _, err := db.ResolvePendingAction("p2", PendingRejected, "op", ""); err != nil {
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
// /approve — the row moves, the reviewer from the body is recorded, and the
// resolution message lands in the HELD call's chat in the same commit.
func TestPendingActionsApprove(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "tg:9")

	rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{"note": "fine", "reviewer": "google:op@x"})
	if rec.Code != 200 {
		t.Fatalf("approve = %d body=%s", rec.Code, rec.Body.String())
	}
	row, ok := db.PendingAction("p1")
	if !ok || row.Status != PendingApproved {
		t.Fatalf("row = %+v, want approved", row)
	}
	if row.ReviewedBy != "google:op@x" || row.ReviewerNote != "fine" {
		t.Fatalf("reviewer not recorded: %+v", row)
	}
	n, content := chatMessages(t, db, "tg:9")
	if n != 1 {
		t.Fatalf("resolution messages in held chat = %d, want 1", n)
	}
	for _, want := range []string{"p1", "send_file", `{"path":"/etc/x"}`} {
		if !strings.Contains(content, want) {
			t.Fatalf("resolution message missing %q: %s", want, content)
		}
	}
}

// TestPendingActionsApproveChatlessFallsBackToFolder: a call held outside any
// chat gets its trigger on the bare folder JID (the delegate shape).
func TestPendingActionsApproveChatlessFallsBackToFolder(t *testing.T) {
	db, h := authSrv(t, nil)
	seedHeld(t, db, "p1", "demo", "")

	if rec := doJSON(t, h, "POST", "/v1/pending_actions/p1/approve", "",
		map[string]string{"reviewer": "op"}); rec.Code != 200 {
		t.Fatalf("approve = %d body=%s", rec.Code, rec.Body.String())
	}
	if n, _ := chatMessages(t, db, "demo"); n != 1 {
		t.Fatalf("trigger on folder JID = %d messages, want 1", n)
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
	row, _ := db.PendingAction("p1")
	if row.Status != PendingRejected {
		t.Fatalf("status = %q, want rejected", row.Status)
	}
	if n, _ := chatMessages(t, db, "tg:9"); n != 0 {
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
	if n, _ := chatMessages(t, db, "tg:9"); n != 1 {
		t.Fatal("second verdict wrote a second trigger")
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
