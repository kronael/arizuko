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
