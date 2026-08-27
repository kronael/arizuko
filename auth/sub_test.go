package auth

import "testing"

// The sub prefix rule (spec 5/1): "user:"/"service:" exists ONLY in the JWT
// sub claim. Every stored principal — acl.principal, acl_membership.parent —
// is bare, so a prefixed subject compared against stored rows matches nothing
// and silently grants nothing. That is exactly how identity pairing shipped
// inert (BUGS V1).
func TestBareSubStripsOnlyTheJWTPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user:alice", "alice"},
		{"service:proxyd", "proxyd"},
		{"user:7", "7"},

		// The reason the implementation is TrimPrefix and not Cut: a provider
		// subject carries its own colon and must pass through untouched. Cut
		// would return "123" and match a different principal, or none.
		{"google:123", "google:123"},
		{"github:octocat", "github:octocat"},
		{"telegram:4815162342", "telegram:4815162342"},

		// Already-bare principals are the common case on the storage side.
		{"alice", "alice"},
		{"", ""},

		// Only a leading prefix is a prefix.
		{"superuser:alice", "superuser:alice"},
		{"x/user:alice", "x/user:alice"},
	}
	for _, c := range cases {
		if got := BareSub(c.in); got != c.want {
			t.Errorf("BareSub(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Bare is a fixed point: the storage side may call it on a value that already
// came through, and a second pass must not eat another segment.
func TestBareSubIsIdempotent(t *testing.T) {
	for _, in := range []string{"user:alice", "service:proxyd", "google:123", "alice"} {
		once := BareSub(in)
		if twice := BareSub(once); twice != once {
			t.Errorf("BareSub(%q) = %q, but BareSub again = %q", in, once, twice)
		}
	}
}
