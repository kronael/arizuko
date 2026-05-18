package store

import (
	"testing"
)

func TestRouteToken_InsertLookup(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	raw := GenRouteToken()
	if err := s.InsertRouteToken(raw, RouteToken{
		JID:         "web:acme",
		OwnerFolder: "acme",
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := s.LookupRouteToken(raw)
	if !ok {
		t.Fatal("lookup failed")
	}
	if got.JID != "web:acme" || got.OwnerFolder != "acme" {
		t.Fatalf("bad lookup: %+v", got)
	}
}

func TestRouteToken_LookupUnknown(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	if _, ok := s.LookupRouteToken("bogus"); ok {
		t.Fatal("unknown token returned ok")
	}
	if _, ok := s.LookupRouteToken(""); ok {
		t.Fatal("empty token returned ok")
	}
}

func TestRouteToken_Validation(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	if err := s.InsertRouteToken("", RouteToken{JID: "web:a", OwnerFolder: "a"}); err == nil {
		t.Fatal("empty token accepted")
	}
	if err := s.InsertRouteToken(GenRouteToken(), RouteToken{JID: "", OwnerFolder: "a"}); err == nil {
		t.Fatal("empty jid accepted")
	}
	if err := s.InsertRouteToken(GenRouteToken(), RouteToken{JID: "bogus:foo", OwnerFolder: "a"}); err == nil {
		t.Fatal("non-web/hook jid accepted")
	}
}

func TestRouteToken_List(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	mustInsert := func(jid, owner string) {
		if err := s.InsertRouteToken(GenRouteToken(), RouteToken{JID: jid, OwnerFolder: owner}); err != nil {
			t.Fatal(err)
		}
	}
	mustInsert("web:acme", "acme")
	mustInsert("hook:acme/github", "acme")
	mustInsert("web:other", "other")

	got := s.ListRouteTokens("acme")
	if len(got) != 2 {
		t.Fatalf("want 2 tokens, got %d", len(got))
	}
	other := s.ListRouteTokens("other")
	if len(other) != 1 {
		t.Fatalf("want 1 token for other, got %d", len(other))
	}
}

func TestRouteToken_Revoke(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	raw := GenRouteToken()
	if err := s.InsertRouteToken(raw, RouteToken{JID: "web:acme", OwnerFolder: "acme"}); err != nil {
		t.Fatal(err)
	}

	// Wrong owner — no rows deleted, no error.
	ok, err := s.RevokeRouteToken("web:acme", "intruder")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong-owner revoke should not succeed")
	}
	if _, ok := s.LookupRouteToken(raw); !ok {
		t.Fatal("token disappeared after wrong-owner revoke")
	}

	// Correct owner — row deleted, lookup fails.
	ok, err = s.RevokeRouteToken("web:acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("correct-owner revoke returned false")
	}
	if _, ok := s.LookupRouteToken(raw); ok {
		t.Fatal("token still resolves after revoke")
	}
}

func TestRouteTokenKind(t *testing.T) {
	cases := []struct{ jid, want string }{
		{"web:acme", "chat"},
		{"web:a/b/c", "chat"},
		{"hook:a/b", "hook"},
		{"telegram:user/1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := RouteTokenKind(tc.jid); got != tc.want {
			t.Errorf("RouteTokenKind(%q)=%q want %q", tc.jid, got, tc.want)
		}
	}
}
