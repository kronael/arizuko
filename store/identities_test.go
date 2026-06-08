package store

import (
	"testing"
)

func TestCreateIdentity_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, err := s.CreateIdentity("alice")
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if idn.ID == "" || len(idn.ID) != 32 {
		t.Errorf("ID = %q, want 32-char hex", idn.ID)
	}
	if idn.Name != "alice" {
		t.Errorf("Name = %q", idn.Name)
	}
	got, ok := s.GetIdentity(idn.ID)
	if !ok {
		t.Fatal("GetIdentity not found")
	}
	if got.Name != "alice" {
		t.Errorf("got Name = %q", got.Name)
	}
}

func TestLinkSub_AndQuery(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, _ := s.CreateIdentity("alice")
	if err := s.LinkSub(idn.ID, "telegram:1234"); err != nil {
		t.Fatalf("LinkSub: %v", err)
	}
	if err := s.LinkSub(idn.ID, "whatsapp:5678@lid"); err != nil {
		t.Fatalf("LinkSub: %v", err)
	}

	gotIdn, subs, ok := s.GetIdentityForSub("telegram:1234")
	if !ok {
		t.Fatal("GetIdentityForSub: not found")
	}
	if gotIdn.ID != idn.ID {
		t.Errorf("identity mismatch: %s vs %s", gotIdn.ID, idn.ID)
	}
	if len(subs) != 2 {
		t.Errorf("subs len = %d, want 2: %v", len(subs), subs)
	}
}

func TestLinkSub_RebindsAcrossIdentities(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	a, _ := s.CreateIdentity("alice")
	b, _ := s.CreateIdentity("bob")

	if err := s.LinkSub(a.ID, "telegram:1234"); err != nil {
		t.Fatalf("LinkSub alice: %v", err)
	}
	if err := s.LinkSub(b.ID, "telegram:1234"); err != nil {
		t.Fatalf("LinkSub bob: %v", err)
	}

	got, _, ok := s.GetIdentityForSub("telegram:1234")
	if !ok {
		t.Fatal("not found after rebind")
	}
	if got.ID != b.ID {
		t.Errorf("rebind didn't take: got %s, want %s", got.ID, b.ID)
	}
}

func TestUnlinkSub(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, _ := s.CreateIdentity("alice")
	s.LinkSub(idn.ID, "telegram:1234")

	removed, err := s.UnlinkSub("telegram:1234")
	if err != nil {
		t.Fatalf("UnlinkSub: %v", err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	if _, _, ok := s.GetIdentityForSub("telegram:1234"); ok {
		t.Error("claim still present after unlink")
	}
	removed2, _ := s.UnlinkSub("telegram:1234")
	if removed2 {
		t.Error("second unlink should be a no-op")
	}
}

func TestListIdentities(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.CreateIdentity("alice")
	s.CreateIdentity("bob")
	all, err := s.ListIdentities()
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("len = %d, want 2", len(all))
	}
}
