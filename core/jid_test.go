package core

import (
	"encoding/json"
	"testing"
)

func TestParseJID_Happy(t *testing.T) {
	cases := []string{
		"telegram:user/12345",
		"telegram:group/67890",
		"discord:5678/9012",
		"discord:dm/54321",
		"discord:user/u123",
		"whatsapp:111@g.us",
		"mastodon:mastodon.social/account42",
		"reddit:comment/abc",
		"reddit:submission/xyz",
		"reddit:user/alice",
		"web:slink/tok-abc",
		"web:user/sub-1",
		"email:address/foo@bar.com",
		"bluesky:user/did%3Aplc%3A123", // percent-encoded colon in DID
	}
	for _, s := range cases {
		j, err := ParseJID(s)
		if err != nil {
			t.Errorf("ParseJID(%q) err=%v", s, err)
			continue
		}
		if j.String() != s {
			t.Errorf("ParseJID(%q).String() = %q", s, j.String())
		}
	}
}

func TestParseJID_Reject(t *testing.T) {
	cases := []string{
		"",          // empty
		":foo",      // empty scheme
		"telegram:", // empty opaque
		"http://x",  // not opaque (has //)
	}
	for _, s := range cases {
		if _, err := ParseJID(s); err == nil {
			t.Errorf("ParseJID(%q) accepted, want error", s)
		}
	}
}

func TestParseChatJID_RequiresKindSegment(t *testing.T) {
	// The empty-kind case is "scheme:" but with /something — i.e. starts with /.
	// path.Cut handles "" before /; but ParseJID rejects empty Opaque first.
	// Simulate: a JID whose first segment is empty would be like "x:/foo",
	// which url.Parse accepts as Opaque="/foo" (Cut yields ""). Verify:
	if _, err := ParseChatJID("telegram:/missing"); err == nil {
		t.Error("ParseChatJID accepted empty first segment")
	}
}

func TestPlatformPathKind(t *testing.T) {
	j, err := ParseChatJID("telegram:group/12345")
	if err != nil {
		t.Fatal(err)
	}
	if j.Platform() != "telegram" {
		t.Errorf("Platform=%q", j.Platform())
	}
	if j.Path() != "group/12345" {
		t.Errorf("Path=%q", j.Path())
	}
	if j.Kind() != "group" {
		t.Errorf("Kind=%q", j.Kind())
	}
}

func TestMatchJID(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"telegram:group/*", "telegram:group/123", true},
		{"telegram:group/*", "telegram:user/123", false},
		{"telegram:group/*", "telegram:group/123/sub", false}, // * doesn't cross /
		{"discord:dm/*", "discord:dm/abc", true},
		{"discord:67890/*", "discord:67890/chan-1", true},
		{"discord:67890/*", "discord:99999/chan-1", false},
		{"whatsapp:*@g.us", "whatsapp:1234@g.us", true},
		{"whatsapp:*@g.us", "whatsapp:1234@s.whatsapp.net", false},
		{"mastodon:mastodon.social/*", "mastodon:mastodon.social/account42", true},
		{"mastodon:mastodon.social/*", "mastodon:mastodon.social/status/abc", false}, // * doesn't cross /
		{"reddit:user/*", "reddit:user/alice", true},
		{"web:slink/*", "web:slink/tok", true},
		{"web:slink/*", "web:user/sub", false},
	}
	for _, tc := range cases {
		got := MatchJID(tc.pattern, tc.value)
		if got != tc.want {
			t.Errorf("MatchJID(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestJID_ScanValue(t *testing.T) {
	in := "telegram:group/12345"
	var j JID
	if err := j.Scan(in); err != nil {
		t.Fatal(err)
	}
	v, err := j.Value()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := v.(string); !ok || s != in {
		t.Errorf("Value() = %v (%T), want %q", v, v, in)
	}

	var z JID
	if err := z.Scan(""); err != nil {
		t.Fatal(err)
	}
	if !z.IsZero() {
		t.Error("scan empty must yield zero JID")
	}
	v, _ = z.Value()
	if v.(string) != "" {
		t.Errorf("zero Value=%v", v)
	}
}

func TestChatJID_ScanValue(t *testing.T) {
	in := "discord:dm/abc"
	var c ChatJID
	if err := c.Scan(in); err != nil {
		t.Fatal(err)
	}
	v, _ := c.Value()
	if v.(string) != in {
		t.Errorf("Value=%v", v)
	}
}

func TestJID_JSON(t *testing.T) {
	type wrap struct {
		Chat ChatJID `json:"chat_jid"`
		User UserJID `json:"sender"`
	}
	src := `{"chat_jid":"telegram:group/123","sender":"telegram:user/456"}`
	var w wrap
	if err := json.Unmarshal([]byte(src), &w); err != nil {
		t.Fatal(err)
	}
	if w.Chat.String() != "telegram:group/123" {
		t.Errorf("chat=%q", w.Chat.String())
	}
	if w.User.String() != "telegram:user/456" {
		t.Errorf("user=%q", w.User.String())
	}
	out, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != src {
		t.Errorf("roundtrip:\n got=%s\nwant=%s", out, src)
	}
}

func TestJID_JSON_EmptyAccepted(t *testing.T) {
	type wrap struct {
		Chat ChatJID `json:"chat_jid"`
	}
	var w wrap
	if err := json.Unmarshal([]byte(`{"chat_jid":""}`), &w); err != nil {
		t.Fatal(err)
	}
	if !w.Chat.IsZero() {
		t.Error("empty json string must yield zero ChatJID")
	}
}
