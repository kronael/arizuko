package routd

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

const progressJID = "slack:T/C/U"

func newProgressTurn(t *testing.T, dl *recDeliverer, returnTo string) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", progressJID, "u1", returnTo); err != nil {
		t.Fatal(err)
	}
	return db, srv
}

func statusRows(t *testing.T, db *DB, jid string) []core.Message {
	t.Helper()
	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []core.Message
	for _, m := range msgs {
		if m.Verb == verbStatus {
			rows = append(rows, m)
		}
	}
	return rows
}

func TestProgress_EqualTextIsNoOp(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	for range 2 {
		if err := srv.mcpSubmitStatus("t1", "verified; writing review"); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 || len(dl.edits) != 0 {
		t.Fatalf("equal text: sends=%d edits=%d, want 1 and 0: %+v %+v",
			len(dl.sends), len(dl.edits), dl.sends, dl.edits)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 {
		t.Fatalf("status rows=%d, want 1", len(rows))
	}
}

func TestProgress_NoEditNoMessage(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dl := &recDeliverer{noEdit: true}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 0 || len(dl.edits) != 0 {
		t.Fatalf("a channel without edit received progress: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	if n := countBots(t, db, progressJID); n != 0 {
		t.Fatalf("rows=%d, want 0", n)
	}
	if got := strings.Count(logs.String(), "progress update dropped"); got != 1 {
		t.Fatalf("debug lines=%d, want 1: %s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "turn_id=t1") {
		t.Fatalf("debug line does not name the turn: %s", logs.String())
	}
}

func TestProgress_SecondUpdateEditsAndRewritesRow(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"first", "second"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	rows := statusRows(t, db, progressJID)
	if len(rows) != 1 || rows[0].Content != "⏳ second" {
		t.Fatalf("row after edit = %+v, want one row reading %q", rows, "⏳ second")
	}
	if len(dl.edits) != 1 || dl.edits[0].content != "⏳ second" {
		t.Fatalf("edits=%+v", dl.edits)
	}
}

func TestProgress_HardEditErrorSurfaces(t *testing.T) {
	dl := &recDeliverer{editErr: errSend}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := srv.mcpSubmitStatus("t1", "b"); !errors.Is(err, errSend) {
		t.Fatalf("hard edit error = %v, want errSend", err)
	}
	if len(dl.sends) != 1 {
		t.Fatalf("hard edit error re-posted: sends=%+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "⏳ a" {
		t.Fatalf("row rewritten although the platform edit failed: %q", rows[0].Content)
	}
}

func TestProgress_RefusedEditPostsNothing(t *testing.T) {
	dl := &recDeliverer{editErr: chanlib.ErrUnsupported}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"a", "b"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 {
		t.Fatalf("ErrUnsupported fell back to a fresh send: %+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 || rows[0].Content != "⏳ a" {
		t.Fatalf("rows=%+v, want the single row unchanged", rows)
	}
}

func TestProgress_ResultStatusAlreadyStreamedPostsOnce(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "verified; writing review"); err != nil {
		t.Fatal(err)
	}
	_, err := srv.recordTurnResult("t1", apiv1.TurnResult{
		TurnID: "t1", SessionID: "sX", Status: "success",
		Result: "<status>verified; writing review</status>the review",
	})
	if err != nil {
		t.Fatal(err)
	}
	var marks int
	for _, m := range dl.sends {
		if strings.HasPrefix(m.text, progressMark) {
			marks++
		}
	}
	if marks != 1 || len(dl.edits) != 0 {
		t.Fatalf("progress posts=%d edits=%d, want 1 and 0: %+v %+v", marks, len(dl.edits), dl.sends, dl.edits)
	}
	if len(dl.sends) != 2 || dl.sends[1].text != "the review" {
		t.Fatalf("the reply was lost: %+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 {
		t.Fatalf("status rows=%d, want 1", len(rows))
	}
}

func TestProgress_FailedResultUpdateStillDeliversReply(t *testing.T) {
	dl := &recDeliverer{editErr: errSend}
	_, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	srv.appendAndDeliver("t1", progressJID, "<status>b</status>the answer")
	if last := dl.sends[len(dl.sends)-1]; last.text != "the answer" {
		t.Fatalf("a failed progress edit withheld the reply: %+v", dl.sends)
	}
}

func TestProgress_FirstSendOwedThenRewrittenNotDuplicated(t *testing.T) {
	dl := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"first", "second"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	rows := statusRows(t, db, progressJID)
	if len(rows) != 1 || rows[0].Content != "⏳ second" {
		t.Fatalf("rows=%+v, want one owed row reading %q", rows, "⏳ second")
	}
	if len(dl.sends) != 1 || len(dl.edits) != 0 {
		t.Fatalf("sends=%d edits=%d, want the one failed send and no edit", len(dl.sends), len(dl.edits))
	}
}

func TestProgress_DelegatedTurnEditsAtTheReturnTarget(t *testing.T) {
	dl := &recDeliverer{}
	const origin = "slack:T/ORIGIN/U"
	db, srv := newProgressTurn(t, dl, origin)

	for _, s := range []string{"a", "b"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 || len(dl.edits) != 1 || dl.edits[0].jid != origin {
		t.Fatalf("sends=%+v edits=%+v, want one send and one edit at %s", dl.sends, dl.edits, origin)
	}
	if rows := statusRows(t, db, origin); len(rows) != 1 || rows[0].Content != "⏳ b" {
		t.Fatalf("rows at the return target = %+v", rows)
	}
}

func TestProgress_IsTheOnlyProgressWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "progress.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []*regexp.Regexp{
			regexp.MustCompile(`verbStatus|progressMark|Verb = "status"`),
			regexp.MustCompile(`\.Typing\([^)]*true\)`),
		} {
			if m := banned.Find(src); m != nil {
				t.Errorf("%s holds %q: progress is declared in progress.go only", f, m)
			}
		}
	}
}

func TestChanDeliverer_CanEditReadsTheCapabilityMap(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	a := newFakeAdapter(t, "pid")
	reg := chanreg.New()
	for name, caps := range map[string]map[string]bool{
		"editd":  {"send_text": true, "edit": true},
		"plaind": {"send_text": true},
	} {
		prefix := name + ":"
		if _, err := reg.Register(name, a.srv.URL, []string{prefix}, caps); err != nil {
			t.Fatal(err)
		}
	}
	d := newChanDeliverer(reg, []string{"muted"}, nil)

	for jid, want := range map[string]bool{
		"editd:1":  true,
		"plaind:1": false,
		"nobody:1": false,
		"muted:1":  false,
	} {
		if got := d.CanEdit(jid); got != want {
			t.Errorf("CanEdit(%s) = %v, want %v", jid, got, want)
		}
	}
}
