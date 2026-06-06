package routd

import (
	"testing"
)

// TestPaneSetEndpoint: POST /v1/pane (messages:write) upserts the pane context
// for a channel into routd's OWN routd.db, and paneHints (SiblingPaneContextJID)
// reads it back — proving the write lands where the prompt path looks. routd
// OWNS pane_sessions (migration 0010); it opens NO sibling messages.db.
func TestPaneSetEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:slakd", scope: []string{"messages:write"}})

	// slakd opens the pane first (its own UpsertPane path) — seed the row so the
	// channel-keyed context write has a target.
	if _, err := db.SQL().Exec(
		`INSERT INTO pane_sessions(team_id,user_id,thread_ts,channel_id,opened_at)
		 VALUES('T1','U99','1700.1','D0XY','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed pane: %v", err)
	}

	rec := doJSON(t, h, "POST", "/v1/pane", "", paneSetBody{
		ChannelID: "D0XY", JID: "slack:T1/channel/C42"})
	if rec.Code != 200 {
		t.Fatalf("POST /v1/pane = %d want 200 body=%s", rec.Code, rec.Body.String())
	}

	// Read it back the way paneHints does.
	ctx, ok := db.SiblingPaneContextJID("D0XY")
	if !ok || ctx != "slack:T1/channel/C42" {
		t.Errorf("SiblingPaneContextJID after POST = (%q,%v), want (slack:T1/channel/C42,true)", ctx, ok)
	}

	// Empty jid clears the context.
	if rec := doJSON(t, h, "POST", "/v1/pane", "", paneSetBody{ChannelID: "D0XY"}); rec.Code != 200 {
		t.Fatalf("POST /v1/pane (clear) = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if ctx, ok := db.SiblingPaneContextJID("D0XY"); !ok || ctx != "" {
		t.Errorf("after clear = (%q,%v), want (\"\",true)", ctx, ok)
	}
}

// TestPaneSetMissingChannel: a request without channel_id is 400.
func TestPaneSetMissingChannel(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:slakd", scope: []string{"messages:write"}})
	rec := doJSON(t, h, "POST", "/v1/pane", "", paneSetBody{JID: "slack:T1/channel/C42"})
	if rec.Code != 400 {
		t.Fatalf("POST /v1/pane without channel_id = %d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// TestPaneSetRequiresWriteScope: a token without messages:write is 403.
func TestPaneSetRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	rec := doJSON(t, h, "POST", "/v1/pane", "", paneSetBody{ChannelID: "D0XY", JID: "x"})
	if rec.Code != 403 {
		t.Fatalf("POST /v1/pane without messages:write = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
}
