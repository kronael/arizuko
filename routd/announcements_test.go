package routd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
)

func announcementFile(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("## [Unreleased]\n\n## ["+version+"] — date\n\n> arizuko "+version+" — title\n>\n> summary\n>\n> • first change\n> • second change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseAnnouncements(t *testing.T) {
	db, loop, runner := recLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=telegram:group/1", Target: "demo"}})
	if err := db.QueueReleaseAnnouncements("missing"); err != nil {
		t.Fatal(err)
	}
	loop.pollOnce()
	rows, err := db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(rows) != 0 || len(runner.runs) != 0 {
		t.Fatalf("pending=%v runs=%v err=%v", rows, runner.runs, err)
	}
	routes := []core.Route{
		{Match: "chat_jid=telegram:group/1", Target: "demo#announce"},
		{Match: "chat_jid=telegram:group/1", Target: "other#announce"},
		{Match: "platform=slack room=T/channel/A", Target: "demo#announce"},
		{Match: "chat_jid=slack:T/channel/B", Target: "demo#announce"},
		{Match: "chat_jid=discord:G/A", Target: "demo#announce"},
		{Match: "chat_jid=discord:G/B", Target: "demo#announce"},
		{Match: "chat_jid=discord:dm/A", Target: "demo#announce"},
		{Match: "chat_jid=discord:dm/B", Target: "demo#announce"},
		{Match: "chat_jid=telegram:*", Target: "demo#announce"},
	}
	doSetRoutes(t, db, routes)
	for i := 0; i < 270; i++ {
		if err := db.PutMessage(core.Message{ID: fmt.Sprintf("normal-%d", i), ChatJID: "telegram:group/1", Timestamp: time.Now().UTC(), Content: "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	path := announcementFile(t, "v1.2.3")
	if err := db.QueueReleaseAnnouncements(path); err != nil {
		t.Fatal(err)
	}
	rows, err = db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(rows) != 5 {
		t.Fatalf("pending=%v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Content != "v1.2.3 — first change\n\nhttps://github.com/kronael/arizuko/blob/main/CHANGELOG.md" || !row.FromMe || !row.BotMsg || row.TurnID != "" || row.RoutedTo != "demo" {
			t.Fatalf("row=%+v", row)
		}
		if _, ok := db.GetTurnContext(row.ID); ok {
			t.Fatal("announcement has turn context")
		}
	}
	for i := 0; i < 270; i++ {
		if err := db.PutMessage(core.Message{ID: fmt.Sprintf("later-%d", i), ChatJID: "telegram:group/1", Timestamp: time.Now().UTC(), Content: "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueueReleaseAnnouncements(path); err != nil {
		t.Fatal(err)
	}
	again, err := db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(again) != 5 {
		t.Fatalf("repeat=%v err=%v", again, err)
	}
	dl := &recDeliverer{failIdem: rows[0].ID}
	retry := NewLoop(db, runner, LoopConfig{Deliver: dl})
	defer retry.StopQueue()
	now := time.Now().Add(time.Hour)
	retry.maybeRetryOutbound(now)
	if err := db.QueueReleaseAnnouncements(path); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingOutbound(now, 100)
	if err != nil || len(pending) != 1 || pending[0].ID != rows[0].ID {
		t.Fatalf("retry pending=%v err=%v", pending, err)
	}
	dl.failIdem = ""
	retry.maybeRetryOutbound(now.Add(time.Hour))
	pending, err = db.PendingOutbound(now.Add(2*time.Hour), 100)
	if err != nil || len(pending) != 0 || len(dl.sends) != 6 {
		t.Fatalf("pending=%v sends=%v err=%v", pending, dl.sends, err)
	}
	if err := db.QueueReleaseAnnouncements(announcementFile(t, "v1.2.4")); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(pending) != 5 || len(runner.runs) != 0 {
		t.Fatalf("next=%v runs=%v err=%v", pending, runner.runs, err)
	}
}

func TestAnnouncementDestinationInvalid(t *testing.T) {
	for _, match := range []string{"chat_jid=slack:bad", "chat_jid=discord:bad", "chat_jid=telegram:group/1 platform=slack"} {
		if _, _, err := announcementDestination(core.Route{Match: match, Target: "demo#announce"}); err == nil {
			t.Errorf("%q accepted", match)
		}
	}
}

func TestReleaseAnnouncementAdapterRegistersAfterStartup(t *testing.T) {
	db, _, runner := recLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=telegram:group/123", Target: "demo#announce"}})
	path := announcementFile(t, "v2.0.0")
	if err := db.QueueReleaseAnnouncements(path); err != nil {
		t.Fatal(err)
	}
	reg := chanreg.New()
	loop := NewLoop(db, runner, LoopConfig{Deliver: NewChannelDeliverer(reg, nil, db.LatestSource)})
	defer loop.StopQueue()
	now := time.Now().Add(time.Hour)
	loop.maybeRetryOutbound(now)
	pending, err := db.PendingOutbound(now, 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	adapter := newFakeAdapter(t, "id")
	registerAdapter(t, reg, "custom-telegram", adapter.srv.URL, "telegram:")
	loop.maybeRetryOutbound(now.Add(time.Hour))
	pending, err = db.PendingOutbound(now.Add(time.Hour), 10)
	if err != nil || len(pending) != 0 || adapter.count() != 1 || len(runner.runs) != 0 {
		t.Fatalf("pending=%v hits=%d runs=%v err=%v", pending, adapter.count(), runner.runs, err)
	}
	if err := db.QueueReleaseAnnouncements(path); err != nil {
		t.Fatal(err)
	}
	loop.maybeRetryOutbound(now.Add(2 * time.Hour))
	if adapter.count() != 1 {
		t.Fatal("delivered release resent")
	}
}
