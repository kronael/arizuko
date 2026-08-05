package routd

import (
	"strings"
	"testing"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// Message actions (spec 5/Z) mutate an already-delivered platform message and
// append no messages row, so until now a delete or an edit left no trace
// anywhere — the spec claimed an audit category `social` that has never existed
// (the enum in audit/log.go is closed and has no such member) and no handler
// emitted anything at all.
//
// Both faces are covered because both are reachable: the agent drives these
// over the MCP socket (routd/mcp.go buildGatedFns) and the REST twin
// (routd/turns.go) is the human/external face. Auditing only one would leave
// the other silent.
//
// Falsifiable, per call site: drop auditSocial from mutate and only the like
// case loses its row; from target and only delete/pin/unpin do; from handleEdit
// and only the REST edit case; from socialTool and only the MCP cases. Every
// other test in the package stays green either way — the row is the whole
// behavior change.

// socialRow is one audit_log row, flattened for assertions.
type socialRow struct {
	category, action, actor, surface string
	resource, folder, turnID         string
	outcome, errMsg, params          string
}

// socialRows returns every audit_log row in the store, oldest first, with every
// column that could hold a leak concatenated into params for the no-content
// assertion.
func socialRows(t *testing.T, db *DB) []socialRow {
	t.Helper()
	rows, err := db.SQL().Query(
		`SELECT category, action, actor, COALESCE(surface,''), COALESCE(resource,''),
		        COALESCE(folder,''), COALESCE(turn_id,''), outcome,
		        COALESCE(error_msg,''), COALESCE(params_summary,'')
		 FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []socialRow
	for rows.Next() {
		var r socialRow
		if err := rows.Scan(&r.category, &r.action, &r.actor, &r.surface, &r.resource,
			&r.folder, &r.turnID, &r.outcome, &r.errMsg, &r.params); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit_log rows: %v", err)
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

func TestSocialActionsAuditedOverREST(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		body     any
		action   string
		resource string
		params   string
	}{
		{"like", apiv1.ReactionRequest{JID: "slack:T/C/U", PlatformID: "p9", Reaction: "🎉"},
			"message.like", "slack:T/C/U/p9", `{"reaction":"🎉"}`},
		{"edit", apiv1.EditRequest{JID: "slack:T/C/U", PlatformID: "p9", Content: "fixed"},
			"message.edit", "slack:T/C/U/p9", ""},
		{"delete", apiv1.TargetRequest{JID: "slack:T/C/U", PlatformID: "p9"},
			"message.delete", "slack:T/C/U/p9", ""},
		{"pin", apiv1.TargetRequest{JID: "slack:T/C/U", PlatformID: "p9"},
			"message.pin", "slack:T/C/U/p9", ""},
		{"unpin", apiv1.TargetRequest{JID: "slack:T/C/U", PlatformID: "p9"},
			"message.unpin", "slack:T/C/U/p9", ""},
		// unpin_all is /unpin with all:true and no target — one platform
		// mechanism, so one action, with the distinguishing fact in params.
		{"unpin", apiv1.TargetRequest{JID: "slack:T/C/U", All: true},
			"message.unpin", "slack:T/C/U", `{"all":true}`},
	} {
		t.Run(tc.endpoint+"/"+tc.action, func(t *testing.T) {
			db, _, srv := socialSrv(t)
			rec := doJSONKey(t, srv.Handler(), "POST", "/v1/turns/t1/"+tc.endpoint, "k1", tc.body)
			if rec.Code != 200 {
				t.Fatalf("%s = %d body=%s", tc.endpoint, rec.Code, rec.Body.String())
			}

			r := onlyRow(t, db)
			if r.category != "agent" {
				t.Errorf("category=%q want agent", r.category)
			}
			if r.action != tc.action {
				t.Errorf("action=%q want %q", r.action, tc.action)
			}
			if r.actor != "folder:demo" {
				t.Errorf("actor=%q want folder:demo", r.actor)
			}
			if r.surface != "rest" {
				t.Errorf("surface=%q want rest", r.surface)
			}
			if r.resource != tc.resource {
				t.Errorf("resource=%q want %q", r.resource, tc.resource)
			}
			if r.folder != "demo" || r.turnID != "t1" {
				t.Errorf("folder/turn = %q/%q want demo/t1", r.folder, r.turnID)
			}
			if r.outcome != "ok" {
				t.Errorf("outcome=%q want ok", r.outcome)
			}
			if r.params != tc.params {
				t.Errorf("params=%q want %q", r.params, tc.params)
			}
		})
	}
}

// The socket is the face the agent actually uses (routd/turns.go: "the agent
// uses the socket; this is the REST face"), so an audit trail that only covered
// the REST twin would be empty in production.
func TestSocialActionsAuditedOverMCP(t *testing.T) {
	cases := []struct {
		name     string
		invoke   func(*Server) error
		action   string
		resource string
		params   string
	}{
		{"like", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Like("slack:T/C/U", "p9", "🎉")
		}, "message.like", "slack:T/C/U/p9", `{"reaction":"🎉"}`},
		{"dislike", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Dislike("slack:T/C/U", "p9")
		}, "message.dislike", "slack:T/C/U/p9", ""},
		{"delete", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Delete("slack:T/C/U", "p9")
		}, "message.delete", "slack:T/C/U/p9", ""},
		{"edit", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Edit("slack:T/C/U", "p9", "fixed")
		}, "message.edit", "slack:T/C/U/p9", ""},
		{"pin", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Pin("slack:T/C/U", "p9")
		}, "message.pin", "slack:T/C/U/p9", ""},
		{"unpin", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Unpin("slack:T/C/U", "p9", false)
		}, "message.unpin", "slack:T/C/U/p9", ""},
		{"unpin_all", func(s *Server) error {
			return s.buildGatedFns(socialTurn()).Unpin("slack:T/C/U", "", true)
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
// The caller still sees the failure (422 / the returned error) — the audit row
// is not a substitute for surfacing it.
func TestSocialAuditRecordsFailure(t *testing.T) {
	db, dl, srv := socialSrv(t)
	dl.editErr = errSend

	rec := doJSONKey(t, srv.Handler(), "POST", "/v1/turns/t1/edit", "k1",
		apiv1.EditRequest{JID: "slack:T/C/U", PlatformID: "p9", Content: "fixed"})
	if rec.Code != 422 {
		t.Fatalf("failed edit = %d want 422 body=%s", rec.Code, rec.Body.String())
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
// content into audit_log. Both faces, because both build the row.
func TestSocialAuditNeverCarriesMessageContent(t *testing.T) {
	const secretText = "board deck leaked to bob@example.com"

	t.Run("rest", func(t *testing.T) {
		db, _, srv := socialSrv(t)
		doJSONKey(t, srv.Handler(), "POST", "/v1/turns/t1/edit", "k1",
			apiv1.EditRequest{JID: "slack:T/C/U", PlatformID: "p9", Content: secretText})
		assertNoContent(t, db, secretText)
	})

	t.Run("mcp", func(t *testing.T) {
		db, _, srv := socialSrv(t)
		if err := srv.buildGatedFns(socialTurn()).Edit("slack:T/C/U", "p9", secretText); err != nil {
			t.Fatal(err)
		}
		assertNoContent(t, db, secretText)
	})
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

// A closed turn is refused before the Deliverer is touched, so nothing was
// mutated and nothing is recorded — the row must mean "this happened", not
// "this was attempted and rejected by us".
func TestSocialAuditSkipsClosedTurn(t *testing.T) {
	db, dl, srv := socialSrv(t)
	if err := db.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}

	rec := doJSONKey(t, srv.Handler(), "POST", "/v1/turns/t1/pin", "k1",
		apiv1.TargetRequest{JID: "slack:T/C/U", PlatformID: "p9"})
	if rec.Code != 409 {
		t.Fatalf("pin on closed turn = %d want 409", rec.Code)
	}
	if got := socialRows(t, db); len(got) != 0 {
		t.Errorf("closed turn wrote %d audit rows: %+v", len(got), got)
	}
	if dl.reacts != 0 {
		t.Errorf("closed turn reached the Deliverer")
	}
}
