package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// Email has no feed, no reactions and no edit. Each of those verbs is a stub,
// and a stub that returns nil claims the action succeeded — the agent then
// tells the user it liked their message when nothing happened. chanreg's
// capability gate normally short-circuits these, but the stubs are the second
// line: an operator who advertises a capability the adapter does not have must
// get a refusal, not silence.
func TestEveryUnimplementedVerbRefusesWithAHint(t *testing.T) {
	s := &server{}
	post, postErr := s.Post(chanlib.PostRequest{})
	fwd, fwdErr := s.Forward(chanlib.ForwardRequest{})
	quote, quoteErr := s.Quote(chanlib.QuoteRequest{})
	file, fileErr := s.SendFile("", "", "", "", "", "", "")
	voice, voiceErr := s.SendVoice("", "", "", "", "")

	byVerb := map[string]error{
		"post":       postErr,
		"like":       s.Like(chanlib.LikeRequest{}),
		"delete":     s.Delete(chanlib.DeleteRequest{}),
		"forward":    fwdErr,
		"quote":      quoteErr,
		"send_file":  fileErr,
		"send_voice": voiceErr,
	}
	for verb, err := range byVerb {
		if err == nil {
			t.Errorf("%s returned nil; the agent would report a success that never happened", verb)
			continue
		}
		if !errors.Is(err, chanlib.ErrUnsupported) {
			t.Errorf("%s = %v, want ErrUnsupported so the agent stops retrying", verb, err)
		}
		// The hint is what lets the agent do the thing another way instead of
		// giving up. Each message names the alternative.
		if !strings.Contains(strings.ToLower(err.Error()), "send") {
			t.Errorf("%s gives no route forward: %q", verb, err)
		}
	}

	for verb, id := range map[string]string{
		"post": post, "forward": fwd, "quote": quote, "send_file": file, "send_voice": voice,
	} {
		if id != "" {
			t.Errorf("%s returned id %q alongside its refusal", verb, id)
		}
	}
}

// Typing is a no-op rather than a refusal: every outbound path calls it
// unconditionally, so an error there would break sends that are fine.
func TestTypingIsASilentNoOp(t *testing.T) {
	(&server{}).Typing("email:thread/1", true)
	(&server{}).Typing("email:thread/1", false)
}
