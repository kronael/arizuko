package auth

import "path"

// MatchGroups reports whether folder is allowed. `**` matches any folder
// (operator is implicit — a `**` grant is the only operator signal),
// other patterns use path.Match. Empty allowed = no access.
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
