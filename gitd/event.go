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
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

type account struct {
	Login string `json:"login"`
}

type comment struct {
	ID                int64  `json:"id"`
	Body              string `json:"body"`
	HTMLURL           string `json:"html_url"`
	Path              string `json:"path"`
	InReplyToID       int64  `json:"in_reply_to_id"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	ReviewID          int64  `json:"pull_request_review_id"`
	DiffHunk          string `json:"diff_hunk"`
	Side              string `json:"side"`
	Line              *int   `json:"line"`
	StartLine         *int   `json:"start_line"`
	OriginalLine      *int   `json:"original_line"`
	OriginalStartLine *int   `json:"original_start_line"`
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
	CheckRun          *ciRun   `json:"check_run"`
	CheckSuite        *ciRun   `json:"check_suite"`
	WorkflowRun       *ciRun   `json:"workflow_run"`
	Issue             *struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		User        *account  `json:"user"`
		Assignees   []account `json:"assignees"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	PullRequest *struct {
		Number    int      `json:"number"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		HTMLURL   string   `json:"html_url"`
		Merged    bool     `json:"merged"`
		UpdatedAt string   `json:"updated_at"`
		User      *account `json:"user"`
		Head      struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Comment    *comment `json:"comment"`
	Discussion *struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	} `json:"discussion"`
	StatusUpdate *struct {
		Body string `json:"body"`
	} `json:"projects_v2_status_update"`
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
var appAcceptsRepo = core.EnvOr("GITHUB_APP_ACCEPTS_REPO", "") == "1"

// checkSource binds the payload to the registered source the path named: a
// repo hook may only speak for its repo, an org hook for its org's repos, and
// an App delivery must carry an installation (its ping precedes any install).
// The secret proves the sender holds the credential; this proves its scope.
func checkSource(kind, name, event string, p *payload) error {
	if p.Repository != nil {
		_, err := store.GitHubSourceKey(store.GitHubSourceRepo, p.Repository.FullName)
		if err != nil || !strings.EqualFold(p.Repository.FullName, p.Repository.Owner.Login+"/"+p.Repository.Name) {
			return errSourceMismatch
		}
	}
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
		if p.Installation != nil && p.Installation.ID <= 0 {
			return errors.New("not an App delivery: invalid installation ID")
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

// authoredText is the text a person wrote with this delivery: what the action
// created or changed, never the object it merely carries (a label event holds
// the PR body too). A mention in it asks for a turn, whatever the event kind.
func (p *payload) authoredText(event string) string {
	written := p.Action == "created" || p.Action == "edited"
	var parts []string
	switch event {
	case "push":
		for _, c := range p.Commits {
			parts = append(parts, c.Message)
		}
	case "issues":
		if p.Issue != nil && (p.Action == "opened" || p.Action == "reopened" || p.Action == "edited") {
			parts = append(parts, p.Issue.Title, p.Issue.Body)
		}
	case "pull_request":
		if p.PullRequest != nil && (p.Action == "opened" || p.Action == "reopened" ||
			p.Action == "ready_for_review" || p.Action == "edited") {
			parts = append(parts, p.PullRequest.Title, p.PullRequest.Body)
		}
	case "pull_request_review":
		if p.Review != nil && (p.Action == "submitted" || p.Action == "edited") {
			parts = append(parts, p.Review.Body)
		}
	case "discussion":
		if p.Discussion != nil && written {
			parts = append(parts, p.Discussion.Title, p.Discussion.Body)
		}
	case "projects_v2_status_update":
		if p.StatusUpdate != nil && written {
			parts = append(parts, p.StatusUpdate.Body)
		}
	case "check_run", "check_suite", "workflow_run":
		if run, _ := p.ciRun(event); run != nil && p.Action == "completed" {
			parts = append(parts, run.authored())
		}
	default:
		if p.Comment != nil && written {
			parts = append(parts, p.Comment.Body)
		}
	}
	return strings.Join(parts, "\n")
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

type location struct {
	kind, thread, review, state, path, lines, side, url, hunk string
	outdated                                                  bool
}

const hunkLines = 20

func (l *location) reviewComment(c *comment) {
	root := c.InReplyToID
	if root == 0 {
		root = c.ID
	}
	l.thread = reviewIDPrefix + strconv.FormatInt(root, 10)
	if c.ReviewID != 0 {
		l.review = "gh-r" + strconv.FormatInt(c.ReviewID, 10)
	}
	l.path, l.side, l.url, l.hunk = c.Path, c.Side, c.HTMLURL, c.DiffHunk
	first, last := c.StartLine, c.Line
	if c.Line == nil && c.OriginalLine != nil {
		first, last, l.outdated = c.OriginalStartLine, c.OriginalLine, true
	}
	switch {
	case last == nil:
	case first == nil || *first == *last:
		l.lines = strconv.Itoa(*last)
	default:
		l.lines = fmt.Sprintf("%d-%d", *first, *last)
	}
}

func (l location) render(id, body string) string {
	var b strings.Builder
	b.WriteString("[github kind=" + l.kind + " id=" + id)
	for _, key := range []struct{ name, value string }{
		{"thread", l.thread}, {"review", l.review}, {"state", l.state},
		{"path", l.path}, {"lines", l.lines},
	} {
		if key.value != "" {
			b.WriteString(" " + key.name + "=" + key.value)
		}
	}
	if l.outdated {
		b.WriteString(" outdated=true")
	}
	for _, key := range []struct{ name, value string }{{"side", l.side}, {"url", l.url}} {
		if key.value != "" {
			b.WriteString(" " + key.name + "=" + key.value)
		}
	}
	b.WriteString("]")
	if hunk := strings.TrimRight(l.hunk, "\n"); hunk != "" {
		lines := strings.Split(hunk, "\n")
		lines = lines[max(0, len(lines)-hunkLines):]
		fence := "```"
		for strings.Contains(hunk, fence) {
			fence += "`"
		}
		b.WriteString("\n" + fence + "diff\n" + strings.Join(lines, "\n") + "\n" + fence)
	}
	return withBody(b.String(), body)
}

func parseTime(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return fallback
}

func (p *payload) isStateAction(event string) bool {
	return (event == "pull_request" && p.Action != "review_requested" &&
		p.Action != "review_request_removed" && p.Action != "assigned" && p.Action != "unassigned") ||
		event == "pull_request_review_thread" ||
		(event == "pull_request_review" && p.Review != nil && (p.Action == "dismissed" ||
			(p.Action == "submitted" && (p.Review.State == "approved" || p.Review.State == "changes_requested"))))
}

func mapEvent(kind, name, event, delivery string, body []byte, now time.Time, botLogin string) (msg chanlib.InboundMsg, drop string, err error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return msg, "", fmt.Errorf("invalid json: %w", err)
	}
	if err := checkSource(kind, name, event, &p); err != nil {
		return msg, "", err
	}
	if event == "security_advisory" {
		return msg, "unsubscribed", nil
	}
	if p.Sender.Login == "" && event != "ping" {
		return msg, "", errors.New("payload has no sender")
	}
	run, isCI := p.ciRun(event)
	if isCI && run == nil {
		return msg, "", errors.New("CI event without its run")
	}
	stateAction := p.isStateAction(event)
	self := botLogin != "" && strings.EqualFold(p.Sender.Login, botLogin)
	metadata := isCI || stateAction || event == "pull_request"
	if p.Sender.Type == "Bot" && !metadata {
		return msg, "bot", nil
	}
	if self && !metadata {
		return msg, "self", nil
	}
	if delivery == "" {
		delivery = chanlib.ShortHash(string(body))
	}
	jid := p.repoJID()
	login := p.Sender.Login
	loc := location{kind: event}
	var conversationAuthor *account
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
		loc.url = p.Issue.HTMLURL
		head := fmt.Sprintf("%s issue #%d: %s", p.Action, p.Issue.Number, p.Issue.Title)
		if p.Action == "opened" || p.Action == "reopened" || p.Action == "edited" {
			if p.Action != "edited" {
				msg.Verb = "message"
			}
			msg.Content = withBody(head, p.Issue.Body)
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
		loc.url = pr.HTMLURL
		head := fmt.Sprintf("%s PR #%d: %s (%s → %s)", p.Action, pr.Number, pr.Title, pr.Head.Ref, pr.Base.Ref)
		switch {
		case p.Action == "opened" || p.Action == "reopened" || p.Action == "ready_for_review" || p.Action == "edited":
			msg.Verb = "message"
			msg.Content = withBody(head, pr.Body)
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
		loc.url = p.Comment.HTMLURL
		jid.Kind, jid.Number = "issue", p.Issue.Number
		if p.Issue.PullRequest != nil {
			jid.Kind = "pr"
		}
		if p.Action == "created" || p.Action == "edited" {
			conversationAuthor = p.Issue.User
		}
		if p.Action == "created" {
			msg.ID = issueIDPrefix + strconv.FormatInt(p.Comment.ID, 10)
			msg.Timestamp = parseTime(p.Comment.CreatedAt, now).Unix()
			msg.Verb = "message"
			msg.Content = p.Comment.Body
		} else {
			msg.Content = fmt.Sprintf("%s %s a comment", login, p.Action)
			if p.Action == "edited" {
				msg.Timestamp = parseTime(p.Comment.UpdatedAt, now).Unix()
				msg.Content = withBody(msg.Content, p.Comment.Body)
			}
		}
	case "pull_request_review_comment":
		if p.PullRequest == nil || p.Comment == nil {
			return msg, "", errors.New("review comment event without pull_request or comment")
		}
		jid.Kind, jid.Number = "pr", p.PullRequest.Number
		loc.reviewComment(p.Comment)
		if p.Action == "created" || p.Action == "edited" {
			conversationAuthor = p.PullRequest.User
		}
		if p.Action == "created" {
			if p.Comment.InReplyToID != 0 {
				msg.ReplyTo = reviewIDPrefix + strconv.FormatInt(p.Comment.InReplyToID, 10)
			}
			msg.ID = reviewIDPrefix + strconv.FormatInt(p.Comment.ID, 10)
			msg.Timestamp = parseTime(p.Comment.CreatedAt, now).Unix()
			msg.Verb = "message"
			msg.Content = p.Comment.Body
		} else {
			msg.Content = fmt.Sprintf("%s %s a review comment on %s", login, p.Action, p.Comment.Path)
			if p.Action == "edited" {
				msg.Timestamp = parseTime(p.Comment.UpdatedAt, now).Unix()
				msg.Content = withBody(msg.Content, p.Comment.Body)
			}
		}
	case "pull_request_review":
		if p.PullRequest == nil || p.Review == nil {
			return msg, "", errors.New("review event without pull_request or review")
		}
		jid.Kind, jid.Number = "pr", p.PullRequest.Number
		loc.url, loc.state = p.Review.HTMLURL, p.Review.State
		if p.Review.ID != 0 {
			loc.review = "gh-r" + strconv.FormatInt(p.Review.ID, 10)
		}
		if p.Action == "submitted" {
			msg.ID = "gh-r" + strconv.FormatInt(p.Review.ID, 10)
			msg.Timestamp = parseTime(p.Review.SubmittedAt, now).Unix()
		}
		head := fmt.Sprintf("%s review (%s) on PR #%d", login, p.Review.State, p.PullRequest.Number)
		if (p.Action == "submitted" || p.Action == "edited") && strings.TrimSpace(p.Review.Body) != "" {
			msg.Verb = "message"
			if p.Action == "submitted" {
				conversationAuthor = p.PullRequest.User
			}
		}
		msg.Content = withBody(head, p.Review.Body)
	default:
		head := login + " " + event
		if p.Action != "" {
			head += " " + p.Action
		}
		msg.Content = withBody(head, p.authoredText(event))
		switch {
		case p.Comment != nil:
			loc.url = p.Comment.HTMLURL
		case p.Discussion != nil:
			loc.url = p.Discussion.HTMLURL
		}
	}
	if isCI {
		loc, msg.Content = p.ciParts(event, run)
	}
	if p.PullRequest != nil && jid.Kind == "" {
		jid.Kind, jid.Number = "pr", p.PullRequest.Number
	}
	if mentions(botLogin, p.authoredText(event)) {
		msg.Verb = "mention"
	}
	if jid.Kind == "pr" && stateAction && (self || p.Sender.Type == "Bot") {
		msg.Verb = event
	}
	if !self && p.Sender.Type == "User" && conversationAuthor.hasLogin(botLogin) {
		msg.Verb = "mention"
	}
	if self {
		msg.Verb = event
	}
	msg.ChatJID = jid.String()
	msg.ChatName = strings.TrimPrefix(msg.ChatJID, "github:")
	msg.Content = loc.render(msg.ID, msg.Content)
	return msg, "", nil
}
