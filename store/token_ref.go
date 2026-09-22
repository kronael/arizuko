package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// TokenRef is a bearer token's non-secret handle: hex(sha256(token)), the one
// scheme for every token family hashed at rest (`invites.ref`,
// `onboarding.token_ref`, `route_tokens.token_hash`). The token is shown once
// at creation; every other surface carries the ref. Full digest, never
// truncated: a prefix could resolve the wrong row. Exported so FS-mounted
// writers (dashd, onbod, the CLI) hash identically.
func TokenRef(token string) string {
	return hex.EncodeToString(TokenRefBytes(token))
}

// TokenRefBytes is TokenRef's unencoded form, for the BLOB column
// (`route_tokens.token_hash`). Same digest: resreg's route-token export relies
// on hex of it being TokenRef.
func TokenRefBytes(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
