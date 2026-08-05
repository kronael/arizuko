package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// TokenRef is a bearer token's non-secret handle: hex(sha256(token)). One
// scheme for every token family this repo hashes at rest — invites
// (`invites.ref`, the primary key), onboarding magic links
// (`onboarding.token_ref`), and route tokens (`route_tokens.token_hash`, via
// TokenRefBytes). The token is a bearer — whoever holds it redeems the grant —
// so it is shown exactly once at creation and every other surface (including
// the DB itself) carries the ref instead. Full digest, never truncated: a
// prefix could collide and resolve the wrong row.
//
// The ref IS the lookup key on those tables, so this is also what the
// create/get/consume paths hash with. Exported so FS-mounted writers outside
// store (dashd, onbod, the CLI) hash identically instead of duplicating it.
func TokenRef(token string) string {
	return hex.EncodeToString(TokenRefBytes(token))
}

// TokenRefBytes is TokenRef's unencoded form: the raw 32 digest bytes, for the
// BLOB column (`route_tokens.token_hash`) rather than the TEXT ones. Same
// scheme, same digest — the two differ only in column encoding, so either is a
// hex.EncodeToString/hex.DecodeString away from the other (which is what
// resreg's route-token export relies on to render token_hash as an invite-style
// ref for YAML transport).
func TokenRefBytes(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
