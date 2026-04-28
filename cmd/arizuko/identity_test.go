package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIdentityLink_NewIdentity(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runIdentityLink(s, "tg:1", "", "alice", &out); err != nil {
		t.Fatalf("link: %v", err)
	}
	if !strings.Contains(out.String(), "created identity") {
		t.Errorf("expected 'created identity', got %q", out.String())
	}
	idn, subs, ok := s.GetIdentityForSub("tg:1")
	if !ok {
		t.Fatal("sub not bound after link")
	}
	if idn.Name != "alice" {
		t.Errorf("name = %q, want alice", idn.Name)
	}
	if len(subs) != 1 || subs[0] != "tg:1" {
		t.Errorf("subs = %v, want [tg:1]", subs)
	}
}

func TestIdentityLink_NameDefaultsToSub(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer
	if err := runIdentityLink(s, "discord:99", "", "", &sink); err != nil {
		t.Fatalf("link: %v", err)
	}
	idn, _, _ := s.GetIdentityForSub("discord:99")
	if idn.Name != "discord:99" {
		t.Errorf("default name = %q, want discord:99", idn.Name)
	}
}

func TestIdentityLink_AddSubToExisting(t *testing.T) {
	s := newMem(t)
	idn, _ := s.CreateIdentity("alice")
	if err := s.LinkSub(idn.ID, "tg:1"); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	var out bytes.Buffer
	if err := runIdentityLink(s, "discord:7", idn.ID, "", &out); err != nil {
		t.Fatalf("link to existing: %v", err)
	}
	if !strings.Contains(out.String(), "linked discord:7 -> "+idn.ID) {
		t.Errorf("output = %q", out.String())
	}

	_, subs, _ := s.GetIdentityForSub("discord:7")
	if len(subs) != 2 {
		t.Errorf("expected 2 subs after add, got %d (%v)", len(subs), subs)
	}
}

func TestIdentityLink_UnknownIDFails(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer
	if err := runIdentityLink(s, "tg:1", "deadbeef", "", &sink); err == nil {
		t.Error("expected error for unknown identity id")
	}
}

func TestIdentityUnlink_Removes(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer
	runIdentityLink(s, "tg:1", "", "alice", &sink)

	var out bytes.Buffer
	if err := runIdentityUnlink(s, "tg:1", &out); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if !strings.Contains(out.String(), "unlinked tg:1") {
		t.Errorf("output = %q", out.String())
	}
	if _, _, ok := s.GetIdentityForSub("tg:1"); ok {
		t.Error("sub still bound after unlink")
	}
}

func TestIdentityUnlink_Missing(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runIdentityUnlink(s, "ghost", &out); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if !strings.Contains(out.String(), "no claim to remove") {
		t.Errorf("output = %q", out.String())
	}
}

func TestIdentityList_EmptyAndPopulated(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runIdentityList(s, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "no identities") {
		t.Errorf("empty list = %q", out.String())
	}

	var sink bytes.Buffer
	runIdentityLink(s, "tg:1", "", "alice", &sink)
	runIdentityLink(s, "discord:7", "", "bob", &sink)

	out.Reset()
	if err := runIdentityList(s, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "alice") || !strings.Contains(got, "bob") {
		t.Errorf("list missing names: %q", got)
	}
	if !strings.Contains(got, "tg:1") || !strings.Contains(got, "discord:7") {
		t.Errorf("list missing subs: %q", got)
	}
}

func TestIdentityLink_EmptySubFails(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer
	if err := runIdentityLink(s, "", "", "alice", &sink); err == nil {
		t.Error("expected error for empty sub")
	}
	if err := runIdentityUnlink(s, "", &sink); err == nil {
		t.Error("expected error for empty sub on unlink")
	}
}
