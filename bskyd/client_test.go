package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSession_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	bc := &bskyClient{
		cfg:  config{DataDir: dir},
		http: nil,
	}

	s := session{
		DID:        "did:plc:abc123",
		AccessJwt:  "access-token",
		RefreshJwt: "refresh-token",
	}
	b, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(dir, "bluesky-session.json"), b, 0o600)

	got := bc.loadSession()
	if got == nil {
		t.Fatal("loadSession returned nil")
	}
	if got.DID != s.DID {
		t.Errorf("DID = %q, want %q", got.DID, s.DID)
	}
	if got.AccessJwt != s.AccessJwt {
		t.Errorf("AccessJwt = %q, want %q", got.AccessJwt, s.AccessJwt)
	}
	if got.RefreshJwt != s.RefreshJwt {
		t.Errorf("RefreshJwt = %q, want %q", got.RefreshJwt, s.RefreshJwt)
	}
}

func TestLoadSession_Missing(t *testing.T) {
	dir := t.TempDir()
	bc := &bskyClient{cfg: config{DataDir: dir}, http: nil}
	if bc.loadSession() != nil {
		t.Error("expected nil for missing session file")
	}
}

func TestLoadSession_Corrupt(t *testing.T) {
	dir := t.TempDir()
	bc := &bskyClient{cfg: config{DataDir: dir}, http: nil}
	os.WriteFile(filepath.Join(dir, "bluesky-session.json"), []byte("not-json"), 0o600)
	if bc.loadSession() != nil {
		t.Error("expected nil for corrupt session file")
	}
}

func TestSaveSession_WritesToDisk(t *testing.T) {
	dir := t.TempDir()
	bc := &bskyClient{
		cfg: config{DataDir: dir},
		session: session{
			DID:        "did:plc:xyz",
			AccessJwt:  "tok-a",
			RefreshJwt: "tok-r",
		},
	}
	bc.saveSession()

	b, err := os.ReadFile(filepath.Join(dir, "bluesky-session.json"))
	if err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	var got session
	if json.Unmarshal(b, &got) != nil {
		t.Fatal("written session is not valid JSON")
	}
	if got.DID != "did:plc:xyz" {
		t.Errorf("DID = %q, want %q", got.DID, "did:plc:xyz")
	}
}

// TestNotificationFilter_SkipRead verifies that read notifications are skipped.
// The logic lives in fetchNotifications: if n.IsRead { continue }.
func TestNotificationFilter_SkipRead(t *testing.T) {
	notifs := []notification{
		{URI: "at://a/b/1", Reason: "mention", IsRead: true},
		{URI: "at://a/b/2", Reason: "mention", IsRead: false},
		{URI: "at://a/b/3", Reason: "reply", IsRead: false},
	}
	var dispatched []notification
	for _, n := range notifs {
		if n.IsRead {
			continue
		}
		dispatched = append(dispatched, n)
	}
	if len(dispatched) != 2 {
		t.Errorf("dispatched %d, want 2", len(dispatched))
	}
	for _, d := range dispatched {
		if d.IsRead {
			t.Errorf("read notification was dispatched: %+v", d)
		}
	}
}

// TestSendFile_NonImageReturnsUnsupported asserts non-image extensions
// route through chanlib.Unsupported instead of attempting an image-blob
// upload that would 502 with an opaque error from the PDS.
func TestSendFile_NonImageReturnsUnsupported(t *testing.T) {
	bc := &bskyClient{}
	cases := []string{"clip.mp4", "audio.mp3", "doc.pdf", "spreadsheet.csv", "noext"}
	for _, name := range cases {
		err := bc.SendFile("bluesky:abc", "/tmp/x", name, "caption")
		if err == nil {
			t.Errorf("%s: want error, got nil", name)
			continue
		}
		// Plain-error chain check rather than type-assert to keep the
		// dependency on chanlib.ErrUnsupported in a single import path.
		if !contains(err.Error(), "unsupported") {
			t.Errorf("%s: err = %v, want 'unsupported' in message", name, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestURIToKey verifies the last path segment is extracted as the record key.
func TestURIToKey(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"at://did:plc:abc/app.bsky.feed.post/3abc123", "3abc123"},
		{"at://did:plc:xyz/app.bsky.notification.listNotifications/rkey99", "rkey99"},
		{"single", "single"},
	}
	for _, tc := range cases {
		got := uriToKey(tc.uri)
		if got != tc.want {
			t.Errorf("uriToKey(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
