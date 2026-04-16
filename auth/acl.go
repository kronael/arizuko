package auth

import "path"

// MatchGroups returns true if folder matches any of the allowed patterns.
// Patterns use path.Match semantics with one extension: "**" matches anything
// (all folders, any depth). Used for grant rules where the operator pattern
// "**" grants universal access. A nil allowed slice means operator
// (unrestricted) and should be handled by the caller — this function
// treats nil/empty as "no access".
func MatchGroups(allowed []string, folder string) bool {
	for _, p := range allowed {
		if p == "**" {
			return true
		}
		if ok, _ := path.Match(p, folder); ok {
			return true
		}
	}
	return false
}
