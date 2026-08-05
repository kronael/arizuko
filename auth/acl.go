package auth

import (
	"log/slog"
	"path"
	"strings"
)

// MatchGroups reports whether any scope pattern in `allowed` covers `folder`.
//
// The pattern IS the containment (5/33 decision 8): `atlas` covers exactly
// `atlas`, `atlas/*` its direct children, `atlas/**` the whole subtree, `**`
// everything. A parent scope does NOT reach a child by position — a path
// carries zero authorization (5/33 decision 2), so an operator who wants
// subtree reach writes `atlas/**` and one who wants a single folder writes
// `atlas`. Empty = no access.
//
// This is the same matcher Authorize applies to `acl.scope`, so every surface
// answers folder containment identically. No caller may bolt a prefix walk
// beside it — that reintroduces depth-derived authority under another name.
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

// MatchSlot reports whether `allowed` covers the folder that OWNS the web-slot
// path `slotPath` — the part of a `/pub|/priv` URL after the prefix.
//
// A slot path is not a folder: folders are multi-segment (`atlas/search` is
// served at `/priv/atlas/search/…`), so the owner cannot be read off segment
// one, and the slot trees nest on disk — `container/runner.go` bind-mounts
// `web/priv/<folder>`, so folder `atlas`'s `~/private_html` physically holds
// `atlas/search`'s slot and every file below it. Any path prefix a scope
// covers therefore admits the whole path under it.
//
// This is 5/V filesystem containment, not grant inheritance: it applies only
// where one folder's mount already holds another's bytes. Folder decisions use
// MatchGroups, which crosses no segment the glob did not ask for.
func MatchSlot(allowed []string, slotPath string) bool {
	slotPath = strings.Trim(slotPath, "/")
	if slotPath == "" {
		return false
	}
	segs := strings.Split(slotPath, "/")
	for i := 1; i <= len(segs); i++ {
		if MatchGroups(allowed, strings.Join(segs[:i], "/")) {
			return true
		}
	}
	return false
}

// matchPattern reports whether the glob `pattern` covers the path `p`.
// A malformed pattern denies, loudly: an operator typo must never widen a
// grant by accident nor narrow one in silence.
func matchPattern(pattern, p string) bool {
	ok, err := matchSegments(
		strings.Split(pattern, "/"),
		strings.Split(p, "/"),
	)
	if err != nil {
		slog.Error("acl: malformed scope glob denies",
			"pattern", pattern, "path", p, "err", err)
		return false
	}
	return ok
}

func matchSegments(pat, in []string) (bool, error) {
	for i, seg := range pat {
		if seg == "**" {
			rest := pat[i+1:]
			if len(rest) == 0 {
				return true, nil
			}
			for j := 0; j <= len(in); j++ {
				ok, err := matchSegments(rest, in[j:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(in) == 0 {
			return false, nil
		}
		ok, err := path.Match(seg, in[0])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		in = in[1:]
	}
	return len(in) == 0, nil
}
