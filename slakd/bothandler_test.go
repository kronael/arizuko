package main

import (
	"errors"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// Slack has no native forward/quote/repost primitives. Each must surface
// a *chanlib.UnsupportedError with a concrete hint pointing at the
// real-world workaround (send/reply_to with quoted text). Without these
// hints the agent retries blindly on every refusal.
func TestBotHandler_UnsupportedHints_Slakd(t *testing.T) {
	b := &bot{}
	cases := []struct {
		name string
		err  error
	}{
		{"forward", mustErrSlakd(b.Forward(chanlib.ForwardRequest{}))},
		{"quote", mustErrSlakd(b.Quote(chanlib.QuoteRequest{}))},
		{"repost", mustErrSlakd(b.Repost(chanlib.RepostRequest{}))},
	}
	for _, c := range cases {
		var ue *chanlib.UnsupportedError
		if !errors.As(c.err, &ue) {
			t.Errorf("%s: want *UnsupportedError, got %v", c.name, c.err)
			continue
		}
		if ue.Hint == "" {
			t.Errorf("%s: empty hint", c.name)
		}
		if ue.Platform != "slack" {
			t.Errorf("%s: platform = %q, want slack", c.name, ue.Platform)
		}
	}
}

// Dislike on Slack is implemented as Like(reaction="thumbsdown") — the
// emoji-reaction surface is the dislike primitive. Assert the alias dispatches
// against the right Slack Web API verb.
func TestBotHandler_DislikeAlias_Slakd(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)

	if err := b.Dislike(chanlib.DislikeRequest{
		ChatJID: "slack:T012/channel/C0HJK", TargetID: "1700000000.000100",
	}); err != nil {
		t.Fatalf("Dislike: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	var found bool
	for _, r := range mock.reacted {
		if r["name"] == "thumbsdown" {
			found = true
		}
	}
	if !found {
		t.Errorf("no reactions.add with thumbsdown; saw %+v", mock.reacted)
	}
}

func mustErrSlakd(_ string, e error) error { return e }
