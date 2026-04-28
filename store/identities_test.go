package store

import (
	"errors"
	"testing"
	"time"
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

func TestMintAndConsumeLinkCode(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, _ := s.CreateIdentity("alice")
	code, err := s.MintLinkCode(idn.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("MintLinkCode: %v", err)
	}
	if code == "" {
		t.Fatal("empty code")
	}

	gotID, err := s.ConsumeLinkCode(code, "discord:9999")
	if err != nil {
		t.Fatalf("ConsumeLinkCode: %v", err)
	}
	if gotID != idn.ID {
		t.Errorf("identity mismatch: %s vs %s", gotID, idn.ID)
	}

	got, _, ok := s.GetIdentityForSub("discord:9999")
	if !ok || got.ID != idn.ID {
		t.Error("sub not linked after consume")
	}

	// Single-shot: second consume of same code fails.
	if _, err := s.ConsumeLinkCode(code, "telegram:111"); !errors.Is(err, ErrLinkCodeInvalid) {
		t.Errorf("second consume err = %v, want ErrLinkCodeInvalid", err)
	}
}

func TestConsumeLinkCode_Expired(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, _ := s.CreateIdentity("alice")
	code, _ := s.MintLinkCode(idn.ID, -1*time.Second) // already expired

	if _, err := s.ConsumeLinkCode(code, "discord:9999"); !errors.Is(err, ErrLinkCodeInvalid) {
		t.Errorf("expired consume err = %v, want ErrLinkCodeInvalid", err)
	}
	if _, _, ok := s.GetIdentityForSub("discord:9999"); ok {
		t.Error("expired code should not have linked sub")
	}
}

func TestConsumeLinkCode_Unknown(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if _, err := s.ConsumeLinkCode("link-deadbeef0000", "x:1"); !errors.Is(err, ErrLinkCodeInvalid) {
		t.Errorf("unknown consume err = %v, want ErrLinkCodeInvalid", err)
	}
}

func TestPruneExpiredLinkCodes(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	idn, _ := s.CreateIdentity("alice")
	s.MintLinkCode(idn.ID, -1*time.Second) // expired
	s.MintLinkCode(idn.ID, 10*time.Minute) // live

	n, err := s.PruneExpiredLinkCodes()
	if err != nil {
		t.Fatalf("PruneExpiredLinkCodes: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
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
