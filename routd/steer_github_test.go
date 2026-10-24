package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// gitd opens every message with a location header, and an inline comment also
// carries its diff hunk (specs/6/27-git-channel.md §"Location header"). Every
// steer handler reads what the person typed after them: read raw, `#deploy` and
// `@child do X` stopped matching and reached the agent as plain text.
const (
	ghHeader     = "[github kind=issue_comment id=gh-c1 url=https://github.com/kronael/arizuko/pull/311#issuecomment-1]\n\n"
	ghInlineHead = "[github kind=pull_request_review_comment id=gh-rc1 thread=gh-rc1 path=gitd/verify.go lines=10 side=RIGHT url=https://github.com/kronael/arizuko/pull/311#discussion_r1]\n" +
		"```diff\n@@ -1,2 +1,3 @@\n+func verify() {}\n```\n\n"
)

func TestSteer_GitHubStickyTopicReadsPastTheHeader(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	loop.deliver = &recDeliverer{}
	putPRRow(t, db, "gh-c1", "github:user/alice", "mention", ghHeader+"#deploy", time.Now().UTC())

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if _, topic := db.StickyState(prJID); topic != "deploy" {
		t.Fatalf("sticky topic=%q want deploy", topic)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("the navigation reached the agent: %+v", rr.runs)
	}
}

func TestSteer_GitHubChildDelegationReadsPastTheHeader(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	if err := db.PutGroup(core.Group{Folder: "eng/child"}); err != nil {
		t.Fatal(err)
	}
	putPRRow(t, db, "gh-c1", "github:user/alice", "mention", ghHeader+"@child do X", time.Now().UTC())

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("the delegation reached the parent agent: %+v", rr.runs)
	}
	msgs, _ := db.MessagesSince("eng/child", "")
	if len(msgs) != 1 {
		t.Fatalf("delegation rows on the child=%d want 1", len(msgs))
	}
	// The child keeps the location, so its reply can name the comment it answers.
	if got := msgs[0].Content; got != strings.TrimSpace(ghHeader)+"\n\ndo X" {
		t.Fatalf("delegated content=%q want the header and the stripped prompt", got)
	}
}

func TestSteer_ASlashCommandAfterAnInlineHunkIsACommand(t *testing.T) {
	m := core.Message{Sender: "github:user/alice", Content: ghInlineHead + "/ping"}
	if !IsSteerCommand(m) {
		t.Fatalf("/ping after the header and the hunk is not a command: %q", cmdText(m.Content))
	}
}

// Only gitd's header is an envelope. A leading [tag] on any other channel is
// text the person typed, so the steer handlers must not read past it.
func TestSteer_ABracketTagOutsideGitHubIsTypedText(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"delegation", "[fyi] @child do X"},
		{"topic pin", "[1] #ops"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, loop, rr := recLoop(t)
			loop.deliver = &recDeliverer{}
			for _, f := range []string{"eng", "eng/child"} {
				if err := db.PutGroup(core.Group{Folder: f}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PutMessage(core.Message{ID: "m1", ChatJID: "web:eng", Sender: "u",
				Content: tc.content, Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if _, err := loop.processGroupMessages("web:eng"); err != nil {
				t.Fatalf("process: %v", err)
			}
			if _, topic := db.StickyState("web:eng"); topic != "" {
				t.Fatalf("sticky topic=%q want none", topic)
			}
			if msgs, _ := db.MessagesSince("eng/child", ""); len(msgs) != 0 {
				t.Fatalf("delegation rows on the child=%d want 0", len(msgs))
			}
			if len(rr.runs) != 1 || !strings.Contains(rr.runs[0].batch, tc.content) {
				t.Fatalf("the text did not reach the agent: %+v", rr.runs)
			}
		})
	}
}

func TestSteer_ASlashCommandReadsPastAnyLeadingTag(t *testing.T) {
	if got := cmdText("[fyi] /ping"); got != "/ping" {
		t.Fatalf("cmdText=%q want /ping", got)
	}
}
