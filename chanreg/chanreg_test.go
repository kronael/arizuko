package chanreg

// Adapter URLs here are private IP literals, not compose service names.
// Register validates the URL with net.LookupIP when the host is a name, and
// that lookup has no timeout: against a resolver that cannot answer, each
// registration in this file waited ~7.7s and the package took 151s. An IP
// literal short-circuits before the resolver and asserts exactly the same
// registry behaviour. validateAdapterURL's own name-resolution branch is
// covered in ssrf_test.go, once.

import (
	"testing"
)

func TestRegisterAndGet(t *testing.T) {
	r := New()

	token, err := r.Register("telegram", "http://10.0.0.11:9001",
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
	if e.URL != "http://10.0.0.11:9001" {
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
	r := New()
	token, _ := r.Register("tg", "http://10.0.0.11:9001", []string{"tg:"}, nil)

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
	r := New()
	token, _ := r.Register("tg", "http://10.0.0.11:9001", []string{"tg:"}, nil)

	r.Deregister("tg")

	if r.Get("tg") != nil {
		t.Error("expected nil after deregister")
	}
	if r.ByToken(token) != nil {
		t.Error("expected nil token lookup after deregister")
	}
}

func TestReRegister(t *testing.T) {
	r := New()
	token1, _ := r.Register("tg", "http://10.0.0.11:9001", []string{"tg:"}, nil)
	token2, _ := r.Register("tg", "http://10.0.0.11:9002", []string{"tg:"}, nil)

	if token1 == token2 {
		t.Error("expected different tokens on re-register")
	}
	if r.ByToken(token1) != nil {
		t.Error("old token should be invalid")
	}
	e := r.ByToken(token2)
	if e == nil || e.URL != "http://10.0.0.11:9002" {
		t.Error("new token should work with updated URL")
	}
}

func TestHealthFails(t *testing.T) {
	r := New()
	r.Register("tg", "http://10.0.0.11:9001", []string{"tg:"}, nil)

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
	r := New()
	r.Register("tg", "http://10.0.0.11:9001", []string{"tg:"}, nil)
	r.Register("discord", "http://10.0.0.13:9002", []string{"discord:"}, nil)

	all := r.All()
	if len(all) != 2 {
		t.Errorf("len = %d, want 2", len(all))
	}
}

func TestHealthFailNonexistent(t *testing.T) {
	r := New()
	if f := r.RecordHealthFail("nope"); f != 0 {
		t.Errorf("fails = %d for nonexistent", f)
	}
}

func TestForJIDSingleAdapter(t *testing.T) {
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)

	e := r.ForJID("telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("ForJID = %+v, want telegram", e)
	}
}

func TestForJIDNoMatch(t *testing.T) {
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)

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
	r := New()
	r.Register("telegram-REDACTED", "http://10.0.0.15:9001", []string{"telegram:"}, nil)
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)

	e := r.ForJID("telegram:123")
	if e == nil {
		t.Fatal("ForJID = nil, want some owner")
	}
	if e.Name != "telegram" && e.Name != "telegram-REDACTED" {
		t.Errorf("ForJID = %s, want one of telegram/telegram-REDACTED", e.Name)
	}
}

func TestForJIDMultiplePrefixes(t *testing.T) {
	r := New()
	r.Register("multi", "http://10.0.0.14:9001", []string{"a:", "b:", "c:"}, nil)

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
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)
	r.Register("telegram-REDACTED", "http://10.0.0.15:9001", []string{"telegram:"}, nil)

	e := r.Resolve("telegram-REDACTED", "telegram:123")
	if e == nil || e.Name != "telegram-REDACTED" {
		t.Fatalf("Resolve = %+v, want telegram-REDACTED by name", e)
	}
}

func TestResolveMissingNameFallsBack(t *testing.T) {
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)

	e := r.Resolve("nonexistent", "telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("Resolve = %+v, want telegram fallback by jid", e)
	}
}

func TestResolveEmptyName(t *testing.T) {
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)

	e := r.Resolve("", "telegram:123")
	if e == nil || e.Name != "telegram" {
		t.Fatalf("Resolve = %+v, want telegram via jid", e)
	}
}

// TestResolveMultiAccountPrecedence guards the multi-bot outbound routing bug:
// two adapters own the same telegram: prefix, distinguished only by name.
// Resolve must pin by name (source-based routing), never first-by-prefix.
func TestResolveMultiAccountPrecedence(t *testing.T) {
	r := New()
	r.Register("telegram", "http://10.0.0.11:9001", []string{"telegram:"}, nil)
	r.Register("telegram-rhias", "http://10.0.0.12:9001", []string{"telegram:"}, nil)

	jid := "telegram:user/42"

	e := r.Resolve("telegram-rhias", jid)
	if e == nil || e.Name != "telegram-rhias" {
		t.Fatalf("Resolve(telegram-rhias) = %+v, want telegram-rhias by name", e)
	}
	if e.URL != "http://10.0.0.12:9001" {
		t.Errorf("resolved URL = %q, want http://10.0.0.12:9001 (wrong account)", e.URL)
	}

	e = r.Resolve("telegram", jid)
	if e == nil || e.Name != "telegram" {
		t.Fatalf("Resolve(telegram) = %+v, want telegram by name", e)
	}

	// Empty name falls back to ForJID (some owner of the prefix).
	e = r.Resolve("", jid)
	if e == nil {
		t.Fatal("Resolve(\"\") = nil, want ForJID fallback to some owner")
	}
	if e.Name != "telegram" && e.Name != "telegram-rhias" {
		t.Errorf("Resolve(\"\") = %s, want one of telegram/telegram-rhias", e.Name)
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

// An adapter restart is a legitimate re-registration, and docker hands the new
// container a new IP. Pinning the entry to that address refused the adapter's
// own return; `chanlib/run.go` exits 1 on a failed Register, so the adapter
// then crash-looped — 156 rejections on krons in 30 days (BUGS L107).
func TestReRegisterSurvivesANewContainerIP(t *testing.T) {
	r := New()
	first, err := r.RegisterWithOrigin("teled", "http://10.0.0.11:8080",
		[]string{"telegram:"}, nil, "172.18.0.4", "s3cret")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	second, err := r.RegisterWithOrigin("teled", "http://10.0.0.11:8080",
		[]string{"telegram:"}, nil, "172.18.0.9", "s3cret")
	if err != nil {
		t.Fatalf("re-register from a new container IP: %v — a restart must not be refused", err)
	}
	if second == first {
		t.Error("re-register returned the same token; the session token must be rotated")
	}
	if e := r.Get("teled"); e == nil || e.OriginIP != "172.18.0.9" {
		t.Errorf("OriginIP = %v, want the new address recorded", e)
	}
}

// The secret is the credential, so the pin must still hold against a caller
// who does not have it — whatever address it calls from.
func TestReRegisterRefusesADifferentSecret(t *testing.T) {
	r := New()
	if _, err := r.RegisterWithOrigin("teled", "http://10.0.0.11:8080",
		[]string{"telegram:"}, nil, "172.18.0.4", "s3cret"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, ip, secret string }{
		{"same address, wrong secret", "172.18.0.4", "guess"},
		{"new address, wrong secret", "172.18.0.9", "guess"},
	} {
		if _, err := r.RegisterWithOrigin("teled", "http://10.0.0.11:8080",
			[]string{"telegram:"}, nil, c.ip, c.secret); err == nil {
			t.Errorf("%s: register succeeded; the principal pin is open", c.name)
		}
	}
}
