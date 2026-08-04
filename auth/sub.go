package auth

import "strings"

// BareSub strips the JWT subject prefix. Spec 5/1's "sub prefix rule": the
// "user:"/"service:" prefix exists ONLY in the JWT sub claim; every stored
// principal — acl.principal, acl_membership.parent — is bare. A prefixed
// subject compared against stored rows matches nothing and silently grants
// nothing, which is exactly how identity pairing shipped inert (BUGS.md V1).
//
// TrimPrefix, not Cut: an already-bare "google:123" must pass through
// unchanged, and Cut would return "123".
func BareSub(sub string) string {
	sub = strings.TrimPrefix(sub, "user:")
	return strings.TrimPrefix(sub, "service:")
}
