package chanlib

import (
	"strconv"
	"strings"
)

// GitHubJID is a parsed GitHub JID (spec 6/27):
//
//	github:<owner>                    account/org-level event (no repository)
//	github:<owner>/<repo>             repo-level event
//	github:<owner>/<repo>/pr/<n>      one pull request
//	github:<owner>/<repo>/issue/<n>   one issue
//
// Owner and repo ride in the JID so one secret (an org hook, an App
// installation) yields one JID per repo and the route table maps each.
type GitHubJID struct {
	Owner  string
	Repo   string
	Kind   string // "" | "pr" | "issue"
	Number int
}

// ParseGitHubJID returns the parsed parts and ok=true on a well-formed JID.
func ParseGitHubJID(jid string) (GitHubJID, bool) {
	rest, found := strings.CutPrefix(jid, "github:")
	if !found || rest == "" {
		return GitHubJID{}, false
	}
	seg := strings.Split(rest, "/")
	for _, s := range seg {
		if s == "" {
			return GitHubJID{}, false
		}
	}
	switch len(seg) {
	case 1:
		return GitHubJID{Owner: seg[0]}, true
	case 2:
		return GitHubJID{Owner: seg[0], Repo: seg[1]}, true
	case 4:
		if seg[2] != "pr" && seg[2] != "issue" {
			return GitHubJID{}, false
		}
		n, err := strconv.Atoi(seg[3])
		if err != nil || n <= 0 {
			return GitHubJID{}, false
		}
		return GitHubJID{Owner: seg[0], Repo: seg[1], Kind: seg[2], Number: n}, true
	}
	return GitHubJID{}, false
}

// FormatGitHubJID is the inverse of ParseGitHubJID. Empty repo → owner level;
// empty kind → repo level.
func FormatGitHubJID(owner, repo, kind string, number int) string {
	jid := "github:" + owner
	if repo == "" {
		return jid
	}
	jid += "/" + repo
	if kind == "" {
		return jid
	}
	return jid + "/" + kind + "/" + strconv.Itoa(number)
}

func (j GitHubJID) String() string {
	return FormatGitHubJID(j.Owner, j.Repo, j.Kind, j.Number)
}

// Topic is the InboundMsg.Topic for this JID: `pr/<n>` or `issue/<n>`, empty
// at repo and owner level. sessions is keyed (folder, topic), so this field is
// what gives two PRs in one folder two sessions.
func (j GitHubJID) Topic() string {
	if j.Kind == "" {
		return ""
	}
	return j.Kind + "/" + strconv.Itoa(j.Number)
}
