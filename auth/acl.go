package auth

import (
	"path"
	"strings"
)

// MatchGroups: `**` = operator/any. `*` does not cross `/`. Empty = no access.
func MatchGroups(allowed []string, folder string) bool {
	for _, p := range allowed {
		if p == "**" {
			return true
		}
		if matchPattern(p, folder) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, folder string) bool {
	return matchSegments(
		strings.Split(pattern, "/"),
		strings.Split(folder, "/"),
	)
}

func matchSegments(pat, in []string) bool {
	for i, seg := range pat {
		if seg == "**" {
			rest := pat[i+1:]
			if len(rest) == 0 {
				return true
			}
			for j := 0; j <= len(in); j++ {
				if matchSegments(rest, in[j:]) {
					return true
				}
			}
			return false
		}
		if len(in) == 0 {
			return false
		}
		if ok, _ := path.Match(seg, in[0]); !ok {
			return false
		}
		in = in[1:]
	}
	return len(in) == 0
}
