package main

import (
	"log/slog"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// storeIdentity answers routd's IdentityResolver off authd's own store. routd
// federates the question because it is not mounted; the CLI is, so it reads the
// declaration authd's identity endpoint is a thin wrapper over —
// authstore.OAuthIdentityForSub. One answer, two readers.
type storeIdentity struct{ st *authstore.Store }

// CreatedAt stays zero: the warning reads only ID and claimed.
func (s storeIdentity) Resolve(sub string) (ipc.Identity, []string, bool, error) {
	id, name, _, subs, ok := s.st.OAuthIdentityForSub(sub)
	if !ok {
		return ipc.Identity{}, nil, false, nil
	}
	return ipc.Identity{ID: id, Name: name}, subs, true, nil
}

// grantPrincipalWarn is the seam runGrant calls. It is a var so a test can
// prove the CLI ASKS — a test that calls the renderer directly passes whether
// or not the grant path ever reaches it, which is how this surface went unwired
// while two others were not.
var grantPrincipalWarn = warnNonCanonical

// warnNonCanonical prints routd's alias warning for a grant the CLI is about to
// write. It is the third and last write surface to read that renderer: add_acl
// and dashd's form already do, and a grant written against a linked alias
// creates a row that can never match — it looks applied and never fires
// (BUGS L152, N2).
//
// FAIL-OPEN, like the other two. A CLI that refuses grants because authd's
// store is unreachable is worse than one that writes them unchecked, and the
// operator is standing right there reading the output.
func warnNonCanonical(principal string, warn func(string)) {
	d, closeStore, err := store.OwnerDriver(store.OwnerAuthd)
	if err != nil {
		slog.Debug("grant: no authd store to check the principal against", "err", err)
		return
	}
	// read-only; close error unactionable
	defer closeStore() //nolint:errcheck
	if w := routd.NonCanonicalWarning(storeIdentity{authstore.New(d)}, principal); w != "" {
		warn(w)
	}
}
