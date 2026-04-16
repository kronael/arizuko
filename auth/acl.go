package auth

import "path"

// MatchGroups reports whether folder is allowed. "**" matches any folder;
// other patterns use path.Match. nil/empty allowed means no access
// (operator = nil is caller's responsibility).
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
