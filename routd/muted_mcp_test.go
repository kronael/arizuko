package routd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

func TestMutedFolderMCPText(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	deliver := &muteDeliverer{recDeliverer: &recDeliverer{pid: "pid"}}
	srv := NewServer(db, nil, deliver, nil, time.Hour, "")
	srv.SetDisabledGroups([]string{"demo"})
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", "out.txt"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	srv.SetDirs(dir, "")
	bound := turnMCP{folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1"}
	sock := t.TempDir() + "/mcp.sock"
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, errText := callToolOverSock(t, sock, "send", map[string]any{"chatJid": jid, "text": "muted"})
	if errText != "" {
		t.Fatal(errText)
	}
	if len(deliver.sends) != 0 {
		t.Fatalf("muted folder delivered %d messages", len(deliver.sends))
	}
	if n := countBots(t, db, jid); n != 1 {
		t.Fatalf("stored %d messages", n)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"reply", map[string]any{"chatJid": jid, "text": "answer"}},
		{"send_file", map[string]any{"chatJid": jid, "filepath": core.ContainerHome + "/out.txt"}},
		{"send_voice", map[string]any{"chatJid": jid, "text": "spoken"}},
		{"post", map[string]any{"chatJid": jid, "content": "post"}},
		{"forward", map[string]any{"sourceMsgId": "source", "targetJid": jid}},
		{"quote", map[string]any{"chatJid": jid, "sourceMsgId": "source", "comment": "quote"}},
		{"repost", map[string]any{"chatJid": jid, "sourceMsgId": "source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errText := callToolOverSock(t, sock, tc.name, tc.args)
			if errText != "" {
				t.Fatal(errText)
			}
		})
	}
	if len(deliver.sends)+len(deliver.docs)+len(deliver.voices)+deliver.posts+deliver.forward+deliver.quote+deliver.repost != 0 {
		t.Fatal("muted creation reached deliverer")
	}
	rows, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("stored %d rows, want seven; forward has no row", len(rows))
	}
	for _, row := range rows {
		if row.PlatformID != "" || statusOf(t, db, row.ID) != core.MessageStatusSent {
			t.Fatalf("muted row: %+v", row)
		}
	}
	if _, ok := db.Engagement(jid, ""); ok {
		t.Fatal("muted send bumped engagement")
	}
}

type muteDeliverer struct {
	*recDeliverer
	forward, quote, repost, panes int
}

func (d *muteDeliverer) SetName(_, _ string) error { d.panes++; return nil }

func (d *muteDeliverer) Forward(_, _, _ string) (string, error) { d.forward++; return "pid", nil }
func (d *muteDeliverer) Quote(_, _, _ string) (string, error)   { d.quote++; return "pid", nil }
func (d *muteDeliverer) Repost(_, _ string) (string, error)     { d.repost++; return "pid", nil }

func TestMutedCreationPaths(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	d := &muteDeliverer{recDeliverer: &recDeliverer{pid: "pid"}}
	s := NewServer(db, nil, d, nil, 0, "")
	s.SetDisabledGroups([]string{"DeMo"})
	if _, err := db.PutTurnContext("t", "demo", "", "slack:foreign", "existing", ""); err != nil {
		t.Fatal(err)
	}
	f := s.buildRoutdFns(turnMCP{folder: "demo", turnID: "t"})
	calls := []struct {
		name string
		call func() (string, error)
	}{
		{"send", func() (string, error) { id, _, err := f.SendMessage("slack:foreign", "x", "1"); return id, err }},
		{"reply", func() (string, error) { id, _, err := f.SendReply("slack:foreign", "x", "", "2"); return id, err }},
		{"file", func() (string, error) {
			id, _, err := f.SendDocument("slack:foreign", "x", "x", "", "", "", "3")
			return id, err
		}},
		{"voice", func() (string, error) {
			id, _, err := f.SendVoice("slack:foreign", "x", "", "demo", "")
			return id, err
		}},
		{"post", func() (string, error) { return f.Post("slack:foreign", "x", nil) }},
		{"forward", func() (string, error) { return f.Forward("x", "slack:foreign", "") }},
		{"quote", func() (string, error) { return f.Quote("slack:foreign", "x", "") }},
		{"repost", func() (string, error) { return f.Repost("slack:foreign", "x") }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.call()
			if err != nil || id != "" {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
	if len(d.sends)+len(d.docs)+len(d.voices)+d.posts+d.forward+d.quote+d.repost != 0 {
		t.Fatal("muted creation reached deliverer")
	}
	if err := f.Like("slack:foreign", "existing", "👍"); err != nil {
		t.Fatal(err)
	}
	if err := f.Edit("slack:foreign", "existing", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete("slack:foreign", "existing"); err != nil {
		t.Fatal(err)
	}
	if d.reacts != 1 || len(d.edits) != 1 || len(d.deletes) != 1 {
		t.Fatal("muted social action did not reach deliverer")
	}
	if err := f.PaneSetTitle("slack:foreign", "title"); err != nil || d.panes != 1 {
		t.Fatalf("pane calls=%d err=%v", d.panes, err)
	}
	if _, err := f.InjectMessage("demo", "internal", "demo", "demo"); err != nil {
		t.Fatal(err)
	}
	f = s.buildRoutdFns(turnMCP{folder: "other", turnID: "t"})
	if id, _, err := f.SendMessage("slack:foreign", "x", "4"); err != nil || id != "pid" {
		t.Fatalf("unmuted id=%q err=%v", id, err)
	}
}

func TestMutedOutboundReplay(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &recDeliverer{failSend: true}
	s := NewServer(db, nil, d, nil, 0, "")
	m := core.Message{ID: "muted-replay", ChatJID: "slack:foreign", Sender: "demo", RoutedTo: "demo", Content: "x", Timestamp: time.Now().UTC(), BotMsg: true}
	if err := db.PutOutbound(m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	pending := pendingDeliveryForTest(t, db, m)
	s.SetDisabledGroups([]string{"demo"})
	NewLoop(db, runnerFn(nil), LoopConfig{Deliver: d}).retryOutbound(time.Now().Add(time.Hour), pending)
	if len(d.sends) != 0 || statusOf(t, db, m.ID) != core.MessageStatusSent {
		t.Fatalf("sends=%v status=%s", d.sends, statusOf(t, db, m.ID))
	}
	if _, ok, err := db.store().PendingDelivery(t.Context(), m); err != nil || ok {
		t.Fatalf("pending=%v err=%v", ok, err)
	}
}

func TestMutedStatusReplay(t *testing.T) {
	d := &recDeliverer{failSend: true}
	db, s := newProgressTurn(t, d, "")
	if err := s.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	row, ok := db.LastStatusRow(progressJID, "t1")
	if !ok {
		t.Fatal("missing status row")
	}
	pending := pendingDeliveryForTest(t, db, row)
	s.SetDisabledGroups([]string{"demo"})
	if err := executeStatus(db, d, pending); err != nil {
		t.Fatal(err)
	}
	if len(d.sends) != 1 {
		t.Fatalf("replayed status: %+v", d.sends)
	}
	if _, ok, err := db.store().PendingDelivery(t.Context(), row); err != nil || ok {
		t.Fatalf("pending=%v err=%v", ok, err)
	}
}
