package chanreg

import (
	"testing"
)

func TestRegisterAndGet(t *testing.T) {
	r := New("secret123")

	token, err := r.Register("telegram", "http://tg:9001",
		[]string{"tg:"}, map[string]bool{"send_text": true})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	e := r.Get("telegram")
	if e == nil {
		t.Fatal("expected entry")
	}
	if e.Name != "telegram" {
		t.Errorf("name = %q, want telegram", e.Name)
	}
	if e.URL != "http://tg:9001" {
		t.Errorf("url = %q", e.URL)
	}
	if !e.HasCap("send_text") {
		t.Error("expected send_text capability")
	}
	if e.HasCap("typing") {
		t.Error("unexpected typing capability")
	}
}

func TestByToken(t *testing.T) {
	r := New("s")
	token, _ := r.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	e := r.ByToken(token)
	if e == nil || e.Name != "tg" {
		t.Fatal("expected to find entry by token")
	}

	e = r.ByToken("bad-token")
	if e != nil {
		t.Fatal("expected nil for bad token")
	}
}

func TestDeregister(t *testing.T) {
	r := New("s")
	token, _ := r.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	r.Deregister("tg")

	if r.Get("tg") != nil {
		t.Error("expected nil after deregister")
	}
	if r.ByToken(token) != nil {
		t.Error("expected nil token lookup after deregister")
	}
}

func TestReRegister(t *testing.T) {
	r := New("s")
	token1, _ := r.Register("tg", "http://tg:9001", []string{"tg:"}, nil)
	token2, _ := r.Register("tg", "http://tg:9002", []string{"tg:"}, nil)

	if token1 == token2 {
		t.Error("expected different tokens on re-register")
	}
	if r.ByToken(token1) != nil {
		t.Error("old token should be invalid")
	}
	e := r.ByToken(token2)
	if e == nil || e.URL != "http://tg:9002" {
		t.Error("new token should work with updated URL")
	}
}

func TestHealthFails(t *testing.T) {
	r := New("s")
	r.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	if f := r.RecordHealthFail("tg"); f != 1 {
		t.Errorf("fails = %d, want 1", f)
	}
	if f := r.RecordHealthFail("tg"); f != 2 {
		t.Errorf("fails = %d, want 2", f)
	}

	r.ResetHealth("tg")
	e := r.Get("tg")
	if e.HealthFails != 0 {
		t.Errorf("health fails = %d after reset", e.HealthFails)
	}
}

func TestAll(t *testing.T) {
	r := New("s")
	r.Register("tg", "http://tg:9001", []string{"tg:"}, nil)
	r.Register("dc", "http://dc:9002", []string{"dc:"}, nil)

	all := r.All()
	if len(all) != 2 {
		t.Errorf("len = %d, want 2", len(all))
	}
}

func TestHealthFailNonexistent(t *testing.T) {
	r := New("s")
	if f := r.RecordHealthFail("nope"); f != 0 {
		t.Errorf("fails = %d for nonexistent", f)
	}
}

func TestForJIDSingleAdapter(t *testing.T) {
	r := New("s")
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	e := r.ForJID("telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("ForJID = %+v, want telegram", e)
	}
}

func TestForJIDNoMatch(t *testing.T) {
	r := New("s")
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	if e := r.ForJID("whatsapp:456"); e != nil {
		t.Errorf("ForJID = %+v, want nil", e)
	}
	if e := r.ForJID(""); e != nil {
		t.Errorf("ForJID empty = %+v, want nil", e)
	}
}

// When multiple adapters share a prefix, ForJID is no longer order-deterministic
// — callers needing exact routing must resolve by name (latest source from the
// messages table). This test only asserts that *some* owning adapter is returned.
func TestForJIDOverlappingPrefixes(t *testing.T) {
	r := New("s")
	r.Register("telegram-REDACTED", "http://REDACTED:9001", []string{"telegram:"}, nil)
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	e := r.ForJID("telegram:123")
	if e == nil {
		t.Fatal("ForJID = nil, want some owner")
	}
	if e.Name != "telegram" && e.Name != "telegram-REDACTED" {
		t.Errorf("ForJID = %s, want one of telegram/telegram-REDACTED", e.Name)
	}
}

func TestForJIDMultiplePrefixes(t *testing.T) {
	r := New("s")
	r.Register("multi", "http://m:9001", []string{"a:", "b:", "c:"}, nil)

	for _, jid := range []string{"a:1", "b:2", "c:3"} {
		if e := r.ForJID(jid); e == nil || e.Name != "multi" {
			t.Errorf("ForJID(%q) = %+v, want multi", jid, e)
		}
	}
	if e := r.ForJID("d:4"); e != nil {
		t.Errorf("ForJID(d:4) = %+v, want nil", e)
	}
}

func TestResolveByName(t *testing.T) {
	r := New("s")
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)
	r.Register("telegram-REDACTED", "http://REDACTED:9001", []string{"telegram:"}, nil)

	e := r.Resolve("telegram-REDACTED", "telegram:123")
	if e == nil || e.Name != "telegram-REDACTED" {
		t.Fatalf("Resolve = %+v, want telegram-REDACTED by name", e)
	}
}

func TestResolveMissingNameFallsBack(t *testing.T) {
	r := New("s")
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	e := r.Resolve("nonexistent", "telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("Resolve = %+v, want telegram fallback by jid", e)
	}
}

func TestResolveEmptyName(t *testing.T) {
	r := New("s")
	r.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	e := r.Resolve("", "telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("Resolve = %+v, want telegram via jid", e)
	}
}

func TestEntryOwns(t *testing.T) {
	e := &Entry{JIDPrefixes: []string{"telegram:", "tg:"}}
	if !e.Owns("telegram:123") {
		t.Error("expected Owns(telegram:123) = true")
	}
	if !e.Owns("tg:456") {
		t.Error("expected Owns(tg:456) = true")
	}
	if e.Owns("whatsapp:789") {
		t.Error("expected Owns(whatsapp:789) = false")
	}
	if e.Owns("") {
		t.Error("expected Owns(empty) = false")
	}
}
