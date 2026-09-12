package routd

import (
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

// BUGS N2: authd resolves a linked OAuth login to the account's CANONICAL sub at
// mint, and grants key on the exact string, so a grant written against a linked
// alias creates a row that can never match — it looks applied and never fires.
// Same shape for a pairing edge naming an alias, which is unreachable to unpair
// too.
//
// The guard is FAIL-OPEN by design (an authd blip must not become a
// control-plane outage over a risk with zero live instances), so the warning has
// to ride back on the RESPONSE. A fail-open guard whose only output is a log
// line is a guard nobody sees.
func TestNonCanonicalWarning(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	claimed := stubIdentity{
		// Alice's canonical is google:X; she has linked github:Y.
		idn:  ipc.Identity{ID: "google:X", Name: "alice"},
		subs: []string{"google:X", "github:Y"},
		sub:  "github:Y",
	}

	t.Run("alias warns and names the canonical", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		srv.SetIdentityResolver(claimed)
		got := srv.nonCanonicalWarning("github:Y")
		if got == "" {
			t.Fatal("granting to a linked alias must warn")
		}
		if !strings.Contains(got, "google:X") {
			t.Errorf("warning must name the canonical sub, got: %q", got)
		}
	})

	t.Run("canonical sub is silent", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		srv.SetIdentityResolver(stubIdentity{
			idn:  ipc.Identity{ID: "google:X", Name: "alice"},
			subs: []string{"google:X"},
			sub:  "google:X",
		})
		if got := srv.nonCanonicalWarning("google:X"); got != "" {
			t.Errorf("canonical sub warned: %q", got)
		}
	})

	t.Run("unclaimed sub is silent", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		srv.SetIdentityResolver(claimed)
		// A sub in no link table resolves to itself — the fleet-wide state today.
		if got := srv.nonCanonicalWarning("discord:user/811"); got != "" {
			t.Errorf("unclaimed sub warned: %q", got)
		}
	})

	t.Run("a role principal is silent", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		srv.SetIdentityResolver(claimed)
		if got := srv.nonCanonicalWarning("role:member"); got != "" {
			t.Errorf("role principal warned: %q", got)
		}
	})

	// The case the entry called out: a guard that stops working exactly when
	// identity data is unavailable is worth less than it reads. Say so rather
	// than implying the principal checked out.
	t.Run("authd unreachable says so", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		srv.SetIdentityResolver(stubIdentity{err: errors.New("authd: connection refused")})
		got := srv.nonCanonicalWarning("github:Y")
		if got == "" {
			t.Fatal("an unreachable authd must warn, not stay silent")
		}
		if !strings.Contains(got, "could not verify") {
			t.Errorf("warning must say it could not verify, got: %q", got)
		}
	})

	// No resolver at all (AUTHD_URL unset, local dev) must not warn on every
	// grant — there is nothing to check against.
	t.Run("no resolver is silent", func(t *testing.T) {
		srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
		if got := srv.nonCanonicalWarning("github:Y"); got != "" {
			t.Errorf("warned with no resolver wired: %q", got)
		}
	})
}

// The grant must still LAND — fail-open, never a refusal, and never a silent
// rewrite of the principal (moving a grant the operator did not ask to move is
// the failure mode N1 chose an immutable canonical sub to avoid).
func TestAddACLToAnAliasStillWritesTheRowAsAsked(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	srv.SetIdentityResolver(stubIdentity{
		idn:  ipc.Identity{ID: "google:X", Name: "alice"},
		subs: []string{"google:X", "github:Y"},
		sub:  "github:Y",
	})

	if err := grantACL(db, core.ACLRow{Principal: "github:Y", Scope: "atlas", Action: "admin", Effect: "allow", GrantedBy: "operator"}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListACL("github:Y")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows for the alias = %d, want 1 (the grant must land as asked)", len(rows))
	}
	if got, err := db.ListACL("google:X"); err != nil || len(got) != 0 {
		t.Errorf("the principal was silently rewritten to the canonical: %+v", got)
	}
}
