package main

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/routd"
)

// A grant written against a linked alias creates a row that can never match a
// token — it looks applied and never fires. routd's acl handler has warned
// about that since N2, but this form writes the store directly, so the operator
// on the web surface was the one person never told (BUGS L152). One renderer
// produces the sentence for both surfaces.
func TestNonCanonicalWarningIsOneRendererForBothSurfaces(t *testing.T) {
	const alias = "google:alice@old"
	const canon = "google:alice"

	warn := routd.NonCanonicalWarning(stubResolver{id: canon, claimed: true}, alias)
	if warn == "" {
		t.Fatal("an aliased principal produced no warning; the row can never match a token")
	}
	for _, want := range []string{alias, canon, "never match"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warning %q does not carry %q, so the operator cannot act on it", warn, want)
		}
	}

	// A principal that IS its own canonical sub must stay silent, or the banner
	// is noise on every correct grant and nobody reads the one that matters.
	if got := routd.NonCanonicalWarning(stubResolver{id: canon, claimed: true}, canon); got != "" {
		t.Errorf("a canonical principal warned anyway: %q", got)
	}
	// No resolver (local dev, no service token) says nothing rather than
	// implying the principal checked out.
	if got := routd.NonCanonicalWarning(nil, alias); got != "" {
		t.Errorf("a nil resolver warned: %q", got)
	}
}

type stubResolver struct {
	id      string
	claimed bool
}

func (s stubResolver) Resolve(string) (ipc.Identity, []string, bool, error) {
	return ipc.Identity{ID: s.id}, nil, s.claimed, nil
}
