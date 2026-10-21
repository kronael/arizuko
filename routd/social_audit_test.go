package routd

import (
	"strings"
	"testing"
)

// Message actions (spec 5/Z-message-actions) mutate an already-delivered platform message and
// append no messages row, so until now a delete or an edit left no trace
// anywhere — the spec claimed an audit category `social` that has never existed
// (the enum in audit/event.go is closed and has no such member) and no handler
// emitted anything at all.
//
// The agent drives these over the MCP socket (routd/mcp.go buildRoutdFns).
//
// Falsifiable: drop auditSocial from socialTool and every case here loses its
// row. Every other test in the package stays green either way — the row is the
// whole behavior change.

// socialRow is one audit_log row, flattened for assertions.
type socialRow struct {
	category, action, actor, surface string
	resource, folder, turnID         string
	outcome, errMsg, params          string
}

// socialRows returns every recorded row in the store, oldest first, with every
// field that could hold a leak available for the no-content assertion.
func socialRows(t *testing.T, db *DB) []socialRow {
	t.Helper()
	newest := allAuditRows(t, db)
	out := make([]socialRow, 0, len(newest))
	for i := len(newest) - 1; i >= 0; i-- {
		r := newest[i]
		out = append(out, socialRow{
			category: r.Category, action: r.Action, actor: r.Actor, surface: r.Surface,
			resource: r.Resource, folder: r.Folder, turnID: r.TurnID,
			outcome: r.Outcome, errMsg: r.ErrorMsg, params: r.ParamsSummary,
		})
	}
	return out
}

// onlyRow asserts exactly one audit row exists and returns it.
func onlyRow(t *testing.T, db *DB) socialRow {
	t.Helper()
	got := socialRows(t, db)
	if len(got) != 1 {
		t.Fatalf("audit_log has %d rows, want exactly 1: %+v", len(got), got)
	}
	return got[0]
}

// socialSrv stands up a routd server with a recording Deliverer and one open
// turn in folder "demo".
func socialSrv(t *testing.T) (*DB, *recDeliverer, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{pid: "pid-1"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", ""); err != nil {
		t.Fatal(err)
	}
	return db, dl, srv
}

// The socket is the face the agent uses, so this is the trail production
// writes.
func TestSocialActionsAuditedOverMCP(t *testing.T) {
	cases := []struct {
		name     string
		invoke   func(*Server) error
		action   string
		resource string
		params   string
	}{
		{"like", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Like("slack:T/C/U", "p9", "🎉")
		}, "message.like", "slack:T/C/U/p9", `{"reaction":"🎉"}`},
		{"dislike", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Dislike("slack:T/C/U", "p9")
		}, "message.dislike", "slack:T/C/U/p9", ""},
		{"delete", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Delete("slack:T/C/U", "p9")
		}, "message.delete", "slack:T/C/U/p9", ""},
		{"edit", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Edit("slack:T/C/U", "p9", "fixed")
		}, "message.edit", "slack:T/C/U/p9", ""},
		{"pin", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Pin("slack:T/C/U", "p9")
		}, "message.pin", "slack:T/C/U/p9", ""},
		{"unpin", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Unpin("slack:T/C/U", "p9", false)
		}, "message.unpin", "slack:T/C/U/p9", ""},
		{"unpin_all", func(s *Server) error {
			return s.buildRoutdFns(socialTurn()).Unpin("slack:T/C/U", "", true)
		}, "message.unpin", "slack:T/C/U", `{"all":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, srv := socialSrv(t)
			if err := tc.invoke(srv); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			r := onlyRow(t, db)
			if r.category != "agent" || r.action != tc.action {
				t.Errorf("(category, action) = (%q, %q) want (agent, %q)", r.category, r.action, tc.action)
			}
			if r.surface != "mcp" {
				t.Errorf("surface=%q want mcp", r.surface)
			}
			if r.actor != "folder:demo" || r.folder != "demo" || r.turnID != "t1" {
				t.Errorf("actor/folder/turn = %q/%q/%q want folder:demo/demo/t1", r.actor, r.folder, r.turnID)
			}
			if r.resource != tc.resource {
				t.Errorf("resource=%q want %q", r.resource, tc.resource)
			}
			if r.params != tc.params {
				t.Errorf("params=%q want %q", r.params, tc.params)
			}
		})
	}
}

func socialTurn() turnMCP {
	return turnMCP{folder: "demo", chatJID: "slack:T/C/U", turnID: "t1"}
}

// A refused mutation is the row worth having: it records that the agent tried.
// The caller still sees the failure (the returned error) — the audit row is not
// a substitute for surfacing it.
func TestSocialAuditRecordsFailure(t *testing.T) {
	db, dl, srv := socialSrv(t)
	dl.editErr = errSend

	if err := srv.buildRoutdFns(socialTurn()).Edit("slack:T/C/U", "p9", "fixed"); err == nil {
		t.Fatal("failed edit returned no error")
	}

	r := onlyRow(t, db)
	if r.action != "message.edit" || r.outcome != "error" {
		t.Errorf("(action, outcome) = (%q, %q) want (message.edit, error)", r.action, r.outcome)
	}
	if !strings.Contains(r.errMsg, "send failed") {
		t.Errorf("error_msg=%q want the adapter error", r.errMsg)
	}
}

// The trail an operator reads is not a copy of the conversation: an edit row
// names the message it changed, never the text it changed it to. Asserted over
// every column, so a later ParamsSummary edit cannot quietly start copying chat
// content into audit_log.
func TestSocialAuditNeverCarriesMessageContent(t *testing.T) {
	const secretText = "board deck leaked to bob@example.com"

	db, _, srv := socialSrv(t)
	if err := srv.buildRoutdFns(socialTurn()).Edit("slack:T/C/U", "p9", secretText); err != nil {
		t.Fatal(err)
	}
	assertNoContent(t, db, secretText)
}

func assertNoContent(t *testing.T, db *DB, text string) {
	t.Helper()
	rows := socialRows(t, db)
	if len(rows) == 0 {
		t.Fatal("edit recorded nothing")
	}
	for _, r := range rows {
		whole := strings.Join([]string{r.category, r.action, r.actor, r.surface,
			r.resource, r.folder, r.turnID, r.outcome, r.errMsg, r.params}, "|")
		if strings.Contains(whole, text) {
			t.Errorf("audit row copies the edited message content: %q", whole)
		}
	}
}
