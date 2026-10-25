package routd

import (
	"testing"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/ipc"
)

func TestProgress_GitdCapsPostNoProgress(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	const jid = "github:kronael/arizuko/pr/12"
	adapter := newFakeAdapter(t, "gh-c1")
	reg := chanreg.New()
	gitdCaps := map[string]bool{"send_text": true, "send_file": true, "typing": true}
	if _, err := reg.Register("gitd", adapter.srv.URL, []string{"github:"}, gitdCaps); err != nil {
		t.Fatal(err)
	}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, newChanDeliverer(reg, nil, nil), nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "working"}); err != nil {
		t.Fatal(err)
	}
	if n := adapter.count(); n != 0 {
		t.Fatalf("gitd received %d sends for a progress update, want 0", n)
	}
	if n := countBots(t, db, jid); n != 0 {
		t.Fatalf("progress rows=%d, want 0", n)
	}
}
