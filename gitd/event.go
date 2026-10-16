package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
)

type account struct {
	Login string `json:"login"`
}

// payload is the subset of every GitHub webhook body gitd reads. One struct
// for all events: the fields absent from an event decode to their zero value.
type payload struct {
	Action     string `json:"action"`
	Zen        string `json:"zen"`
	Ref        string `json:"ref"`
	Forced     bool   `json:"forced"`
	Repository *struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Organization *struct {
		Login string `json:"login"`
	} `json:"organization"`
	Installation *struct {
		ID      int64 `json:"id"`
		Account *struct {
			Login string `json:"login"`
		} `json:"account"`
	} `json:"installation"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	RequestedReviewer *account `json:"requested_reviewer"`
	Assignee          *account `json:"assignee"`
	Issue             *struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	PullRequest *struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		HTMLURL   string `json:"html_url"`
		Merged    bool   `json:"merged"`
		UpdatedAt string `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Comment *struct {
		ID          int64  `json:"id"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		Path        string `json:"path"`
		InReplyToID int64  `json:"in_reply_to_id"`
		CreatedAt   string `json:"created_at"`
	} `json:"comment"`
	Review *struct {
		ID          int64  `json:"id"`
		Body        string `json:"body"`
		State       string `json:"state"`
		HTMLURL     string `json:"html_url"`
		SubmittedAt string `json:"submitted_at"`
	} `json:"review"`
	Commits []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"commits"`
	HeadCommit *struct {
		Timestamp string `json:"timestamp"`
	} `json:"head_commit"`
}

var errSourceMismatch = errors.New("payload repository outside the registered source")

// appAcceptsRepo lets an App-kind source also accept a plain repository
// delivery, which carries no installation. GITHUB_APP_ACCEPTS_REPO sets it.
// The secret still has to verify, so the sender holds the credential, but the
// delivery is no longer bound to an installation the App owns: any repository
// holding that secret can speak on the source's path. Spec 6/27 and BUGS L409.
var appAcceptsRepo = chanlib.EnvOr("GITHUB_APP_ACCEPTS_REPO", "") == "1"

// checkSource binds the payload to the registered source the path named: a
// repo hook may only speak for its repo, an org hook for its org's repos, and
// an App delivery must carry an installation (its ping precedes any install).
// The secret proves the sender holds the credential; this proves its scope.
func checkSource(kind, name, event string, p *payload) error {
	switch kind {
	case store.GitHubSourceRepo:
		if p.Repository == nil || !strings.EqualFold(p.Repository.FullName, name) {
			return errSourceMismatch
		}
	case store.GitHubSourceOrg:
		switch {
		case p.Repository != nil:
			if !strings.EqualFold(p.Repository.Owner.Login, name) {
				return errSourceMismatch
			}
		case p.Organization != nil:
			if !strings.EqualFold(p.Organization.Login, name) {
				return errSourceMismatch
			}
		default:
			return errSourceMismatch
		}
	case store.GitHubSourceApp:
		if p.Installation == nil && event != "ping" && !appAcceptsRepo {
			return errors.New("not an App delivery: no installation in payload")
		}
	default:
		return fmt.Errorf("unknown source kind %q", kind)
	}
	return nil
}

// ownerLogin names the account an event without a repository belongs to.
func (p *payload) ownerLogin() string {
	switch {
	case p.Organization != nil && p.Organization.Login != "":
		return p.Organization.Login
	case p.Installation != nil && p.Installation.Account != nil && p.Installation.Account.Login != "":
		return p.Installation.Account.Login
	}
	return p.Sender.Login
}

func (p *payload) repoJID() chanlib.GitHubJID {
	if p.Repository == nil {
		return chanlib.GitHubJID{Owner: p.ownerLogin()}
	}
	return chanlib.GitHubJID{Owner: p.Repository.Owner.Login, Repo: p.Repository.Name}
}

func mentions(login, text string) bool {
	if login == "" {
		return false
	}
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])@` + regexp.QuoteMeta(login) + `(?:[^A-Za-z0-9-]|$)`).MatchString(text)
}

func speech(login, text string) string {
	if mentions(login, text) {
		return "mention"
	}
	return "message"
}

func (a *account) hasLogin(login string) bool {
	return a != nil && login != "" && strings.EqualFold(a.Login, login)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func withBody(head, body string) string {
	if strings.TrimSpace(body) == "" {
		return head
	}
	return head + "\n\n" + body
}

func parseTime(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return fallback
}

// mapEvent turns one delivery into the InboundMsg routd ingests. Human-authored
// content (a comment, a new PR or issue) carries verb=message, or verb=mention
// when it @-mentions botLogin. A review requested from botLogin or an
// assignment to it carries no text but is a direct ask, so it is a mention
// too. Every other event carries its GitHub event name as the verb and a
// one-line body, so a route row can trigger on the former and observe the
// latter (spec 6/27 decision 4). A bot sender returns drop != "" and no
// message.
func mapEvent(kind, name, event, delivery string, body []byte, now time.Time, botLogin string) (msg chanlib.InboundMsg, drop string, err error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return msg, "", fmt.Errorf("invalid json: %w", err)
	}
	if err := checkSource(kind, name, event, &p); err != nil {
		return msg, "", err
	}
	if p.Sender.Login == "" && event != "ping" {
		return msg, "", errors.New("payload has no sender")
	}
	if p.Sender.Type == "Bot" {
		return msg, "bot", nil
	}
	if delivery == "" {
		delivery = chanlib.ShortHash(string(body))
	}
	jid := p.repoJID()
	login := p.Sender.Login
	msg = chanlib.InboundMsg{
		ID:         "gh-d" + delivery,
		Sender:     "github:" + login,
		SenderName: login,
		Timestamp:  now.Unix(),
		Verb:       event,
		IsGroup:    true,
	}
	switch event {
	case "ping":
		msg.Content = "ping: " + p.Zen
		if login == "" {
			msg.Sender, msg.SenderName = "github:"+jid.Owner, jid.Owner
		}
	case "push":
		var b strings.Builder
		fmt.Fprintf(&b, "%s pushed %d commit(s) to %s", login, len(p.Commits), p.Ref)
		if p.Forced {
			b.WriteString(" (forced)")
		}
		for _, c := range p.Commits {
			sha := c.ID
			if len(sha) > 7 {
				sha = sha[:7]
			}
			fmt.Fprintf(&b, "\n- %s %s", sha, firstLine(c.Message))
		}
		msg.Content = b.String()
		if p.HeadCommit != nil {
			msg.Timestamp = parseTime(p.HeadCommit.Timestamp, now).Unix()
		}
	case "issues":
		if p.Issue == nil {
			return msg, "", errors.New("issues event without issue")
		}
		jid.Kind, jid.Number = "issue", p.Issue.Number
		head := fmt.Sprintf("%s issue #%d: %s", p.Action, p.Issue.Number, p.Issue.Title)
		if p.Action == "opened" || p.Action == "reopened" {
			msg.Verb = speech(botLogin, p.Issue.Body)
			msg.Content = withBody(head+"\n"+p.Issue.HTMLURL, p.Issue.Body)
		} else {
			if p.Action == "assigned" && p.Assignee.hasLogin(botLogin) {
				msg.Verb = "mention"
			}
			msg.Content = login + " " + head
		}
	case "pull_request":
		if p.PullRequest == nil {
			return msg, "", errors.New("pull_request event without pull_request")
		}
		pr := p.PullRequest
		jid.Kind, jid.Number = "pr", pr.Number
		msg.Timestamp = parseTime(pr.UpdatedAt, now).Unix()
		head := fmt.Sprintf("%s PR #%d: %s (%s → %s)", p.Action, pr.Number, pr.Title, pr.Head.Ref, pr.Base.Ref)
		switch {
		case p.Action == "opened" || p.Action == "reopened" || p.Action == "ready_for_review":
			msg.Verb = speech(botLogin, pr.Body)
			msg.Content = withBody(head+"\n"+pr.HTMLURL, pr.Body)
		case p.Action == "closed" && pr.Merged:
			msg.Content = fmt.Sprintf("%s merged PR #%d: %s", login, pr.Number, pr.Title)
		default:
			if p.Action == "review_requested" && p.RequestedReviewer.hasLogin(botLogin) ||
				p.Action == "assigned" && p.Assignee.hasLogin(botLogin) {
				msg.Verb = "mention"
			}
			msg.Content = login + " " + head
		}
	case "issue_comment":
		if p.Issue == nil || p.Comment == nil {
			return msg, "", errors.New("issue_comment event without issue or comment")
		}
		jid.Kind, jid.Number = "issue", p.Issue.Number
		if p.Issue.PullRequest != nil {
			jid.Kind = "pr"
		}
		msg.ID = "gh-c" + strconv.FormatInt(p.Comment.ID, 10)
		msg.Timestamp = parseTime(p.Comment.CreatedAt, now).Unix()
		if p.Action == "created" {
			msg.Verb = speech(botLogin, p.Comment.Body)
			msg.Content = p.Comment.Body
		} else {
			msg.Content = fmt.Sprintf("%s %s a comment", login, p.Action)
		}
	case "pull_request_review_comment":
		if p.PullRequest == nil || p.Comment == nil {
			return msg, "", errors.New("review comment event without pull_request or comment")
		}
		jid.Kind, jid.Number = "pr", p.PullRequest.Number
		msg.ID = reviewIDPrefix + strconv.FormatInt(p.Comment.ID, 10)
		msg.Timestamp = parseTime(p.Comment.CreatedAt, now).Unix()
		if p.Comment.InReplyToID != 0 {
			msg.ReplyTo = reviewIDPrefix + strconv.FormatInt(p.Comment.InReplyToID, 10)
		}
		if p.Action == "created" {
			msg.Verb = speech(botLogin, p.Comment.Body)
			msg.Content = withBody("on "+p.Comment.Path+":", p.Comment.Body)
		} else {
			msg.Content = fmt.Sprintf("%s %s a review comment on %s", login, p.Action, p.Comment.Path)
		}
	case "pull_request_review":
		if p.PullRequest == nil || p.Review == nil {
			return msg, "", errors.New("review event without pull_request or review")
		}
		jid.Kind, jid.Number = "pr", p.PullRequest.Number
		msg.ID = "gh-r" + strconv.FormatInt(p.Review.ID, 10)
		msg.Timestamp = parseTime(p.Review.SubmittedAt, now).Unix()
		head := fmt.Sprintf("%s review (%s) on PR #%d", login, p.Review.State, p.PullRequest.Number)
		if p.Action == "submitted" && strings.TrimSpace(p.Review.Body) != "" {
			msg.Verb = speech(botLogin, p.Review.Body)
		}
		msg.Content = withBody(head, p.Review.Body)
	default:
		head := login + " " + event
		if p.Action != "" {
			head += " " + p.Action
		}
		msg.Content = head
	}
	msg.ChatJID = jid.String()
	msg.Topic = jid.Topic()
	msg.ChatName = strings.TrimPrefix(msg.ChatJID, "github:")
	return msg, "", nil
}
