package store

import (
	"context"
	"testing"
)

// A sub with no user_profiles item is uncapped, not an error. Every platform
// sender and service:routd arrives without one, and they are the subs cost
// attribution actually carries (BUGS L69) — returning an error here would gate
// every such turn to zero spend.
func TestUserCap_NoProfileRowIsUncapped(t *testing.T) {
	s := openEntities(t)
	got, err := s.UserCap("telegram:user/999")
	if err != nil {
		t.Fatalf("UserCap on an absent sub must not error: %v", err)
	}
	if got != 0 {
		t.Fatalf("UserCap = %d, want 0 (uncapped)", got)
	}
}

func TestUserCap_RoundTrip(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("u1", "u1", "User One"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserCap("u1", 250); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserCap("u1")
	if err != nil || got != 250 {
		t.Fatalf("UserCap = %d err=%v, want 250", got, err)
	}
	if err := s.SetUserCap("u1", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserCap("u1"); got != 0 {
		t.Fatalf("after uncapping, UserCap = %d, want 0", got)
	}
}

// Uncapping TAKES THE ATTRIBUTE AWAY rather than storing "0". Absent is the only
// null this model has, so a stored zero is a value every presence test finds —
// and this is the read that would find it.
func TestUncappingLeavesNoAttributeBehind(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("u1", "u1", "User One"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserCap("u1", 250); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserCap("u1", 0); err != nil {
		t.Fatal(err)
	}
	key, err := profileKey("u1")
	if err != nil {
		t.Fatal(err)
	}
	it, ok, err := s.itemAt(context.Background(), UserProfiles, key)
	if err != nil || !ok {
		t.Fatalf("the profile is gone: ok=%v err=%v", ok, err)
	}
	if raw, has := it.Attrs[attrProfCostCap]; has {
		t.Errorf("the uncapped profile still carries %s=%q", attrProfCostCap, raw)
	}
}

// A sub holds one profile. The coordinate is the uniqueness, and a second
// create must say so rather than overwrite the name and the cap the first one
// carries.
func TestASecondProfileForOneSubIsRefused(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUserProfile("github:alice", "alice2", "Someone Else"); err == nil {
		t.Fatal("a second profile for one sub was accepted; it would have replaced the name")
	}
	name, _, err := s.Username("github:alice")
	if err != nil || name != "alice" {
		t.Fatalf("username = %q err=%v, want alice: the refused create still wrote", name, err)
	}
}

// THE UNIQUE USERNAME IS THE READER'S WORK NOW, and this is the case the folder
// check does not catch: two people under DIFFERENT parents pick one name, so
// `parent + "/" + username` collides for neither of them and only the username
// itself is taken. Drop the check in refuseTakenUsername and this goes red.
func TestAUsernameAnotherSubHoldsIsRefused(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("github:alice", "bob", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUserProfile("github:bob", "bob", "Bob"); err == nil {
		t.Fatal("two subs were given one username; the folder check does not catch that pair")
	}
	// The rename door is the one world creation uses, so it carries the same
	// refusal — and it is the door that mattered, because a world is named after
	// the username the picker takes.
	if err := s.CreateUserProfile("github:bob", "bobby", "Bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsername(context.Background(), "github:bob", "bob"); err == nil {
		t.Fatal("a rename onto another sub's username was accepted")
	}
	name, _, err := s.Username("github:bob")
	if err != nil || name != "bobby" {
		t.Fatalf("username = %q err=%v, want bobby: the refused rename still wrote", name, err)
	}
}

// A person may keep their own username: the check names the OTHER holder, not
// the value, so an idempotent re-write of one's own name is not a collision.
func TestASubKeepsItsOwnUsername(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsername(context.Background(), "github:alice", "alice"); err != nil {
		t.Fatalf("a sub was refused its own username: %v", err)
	}
}

// The principals datalist reads the whole partition, because the question names
// nobody. A profile with no display name is still a principal a grant may name —
// dropping it would hide a person from the form that grants to them.
func TestProfileNamesAnswersEveryProfileIncludingTheUnnamed(t *testing.T) {
	s := openEntities(t)
	if err := s.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUserProfile("github:bob", "bob", ""); err != nil {
		t.Fatal(err)
	}
	names, err := s.ProfileNames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("ProfileNames = %v, want both subs", names)
	}
	if names["github:alice"] != "Alice" {
		t.Errorf("alice's label is %q, want Alice", names["github:alice"])
	}
	if label, listed := names["github:bob"]; !listed || label != "" {
		t.Errorf("bob is listed=%v label=%q; a person with no display name is still a "+
			"principal a grant may name", listed, label)
	}
}
