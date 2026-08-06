package store

// InsertOnboarding is both the row's creator and, since the fold (spec 5/31
// § Deleting "greet once, ever"), the re-greet trigger. It fires from routd's
// route-MISS branch, so every call means "this chat spoke and still routes
// nowhere" — the only fact that justifies handing out another link.

import (
	"database/sql"
	"testing"
	"time"
)

func onboardingRow(t *testing.T, s *Store, jid string) (status string, promptedAt sql.NullString) {
	t.Helper()
	if err := s.db.QueryRow(
		`SELECT status, prompted_at FROM onboarding WHERE jid = ?`, jid,
	).Scan(&status, &promptedAt); err != nil {
		t.Fatalf("read onboarding row %s: %v", jid, err)
	}
	return status, promptedAt
}

func setPrompted(t *testing.T, s *Store, jid string, ago time.Duration) string {
	t.Helper()
	ts := time.Now().UTC().Add(-ago).Format(time.RFC3339)
	if _, err := s.db.Exec(
		`UPDATE onboarding SET prompted_at = ? WHERE jid = ?`, ts, jid); err != nil {
		t.Fatalf("stamp prompted_at: %v", err)
	}
	return ts
}

func TestInsertOnboarding_CreatesAnUnpromptedRow(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	status, prompted := onboardingRow(t, s, "telegram:1")
	if status != "awaiting_message" || prompted.Valid {
		t.Errorf("new row = (%q, %v), want (awaiting_message, NULL)", status, prompted)
	}
}

// The permanent lockout blocker 2 named: a link that expired left the user with
// no way to ask for another, because the jid primary key blocks a fresh row and
// the greeter only ever claims prompted_at IS NULL. A miss after PairingTTL now
// re-arms it.
func TestInsertOnboarding_ReGreetsAfterPairingTTL(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	setPrompted(t, s, "telegram:1", PairingTTL+time.Minute)

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	if _, prompted := onboardingRow(t, s, "telegram:1"); prompted.Valid {
		t.Errorf("prompted_at = %q after a miss past the cooldown; want NULL (re-armed)",
			prompted.String)
	}
}

// Within the cooldown the live link is still redeemable, so a second one would
// be pure noise. This is the spam bound: one live link at a time.
func TestInsertOnboarding_DoesNotReGreetWithinPairingTTL(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	stamped := setPrompted(t, s, "telegram:1", PairingTTL/2)

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	_, prompted := onboardingRow(t, s, "telegram:1")
	if !prompted.Valid || prompted.String != stamped {
		t.Errorf("prompted_at = %v, want it untouched at %q", prompted, stamped)
	}
}

// A row that already reached an admission verdict is not a greeting candidate.
// Re-arming it would greet a user who has been queued, approved, or refused —
// and, worse, hand a fresh pairing link to a REFUSED identity.
func TestInsertOnboarding_NeverReArmsAnAdmittedRow(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	for _, status := range []string{"queued", "approved", "refused"} {
		jid := "telegram:" + status
		if err := s.InsertOnboarding(jid); err != nil {
			t.Fatal(err)
		}
		stamped := setPrompted(t, s, jid, PairingTTL+time.Hour)
		if _, err := s.db.Exec(
			`UPDATE onboarding SET status = ?, user_sub = 'github:alice' WHERE jid = ?`,
			status, jid); err != nil {
			t.Fatal(err)
		}

		if err := s.InsertOnboarding(jid); err != nil {
			t.Fatal(err)
		}
		got, prompted := onboardingRow(t, s, jid)
		if got != status {
			t.Errorf("%s row moved to %q", status, got)
		}
		if !prompted.Valid || prompted.String != stamped {
			t.Errorf("%s row re-armed: prompted_at %q -> %v", status, stamped, prompted)
		}
	}
}

// The re-greet must not resurrect the row's other facts. `created` is when the
// chat first appeared, not when it last spoke.
func TestInsertOnboarding_ReGreetKeepsCreated(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	var created string
	s.db.QueryRow(`SELECT created FROM onboarding WHERE jid='telegram:1'`).Scan(&created)
	setPrompted(t, s, "telegram:1", PairingTTL+time.Minute)

	if err := s.InsertOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	var after string
	s.db.QueryRow(`SELECT created FROM onboarding WHERE jid='telegram:1'`).Scan(&after)
	if after != created {
		t.Errorf("created moved: %q -> %q", created, after)
	}
}
