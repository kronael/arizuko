package store

import "testing"

// A sub with no user_profiles row is uncapped, not an error. Every platform
// sender and service:routd arrives without one, and they are the subs cost
// attribution actually carries (BUGS L69) — returning an error here would gate
// every such turn to zero spend.
func TestUserCap_NoProfileRowIsUncapped(t *testing.T) {
	s := openMem(t)
	got, err := s.UserCap("telegram:user/999")
	if err != nil {
		t.Fatalf("UserCap on an absent sub must not error: %v", err)
	}
	if got != 0 {
		t.Fatalf("UserCap = %d, want 0 (uncapped)", got)
	}
}

func TestUserCap_RoundTrip(t *testing.T) {
	s := openMem(t)
	if err := s.CreateAuthUser("u1", "u1", "User One"); err != nil {
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
