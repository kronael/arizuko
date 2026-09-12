package main

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// operatorInject (follow=false) writes the message straight into the DB as an
// inbound on web:<folder> so the gateway poll loop runs the agent — no chat
// token. Verify the injected row's shape (the gateway routes web:<folder> 1:1
// to the group and runs it because no observe route applies).
func TestOperatorInject_Folder(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	if err := s.PutGroup(core.Group{Folder: "krons"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	operatorInject(s, "krons", "operator", "draft me 3 tweets", "", false)

	// Reads back as an inbound on web:krons — the only row the chat holds.
	msgs, err := s.MessagesSince("web:krons", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("injected msgs = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Content != "draft me 3 tweets" {
		t.Errorf("content = %q", m.Content)
	}
	if m.Verb != "mention" {
		t.Errorf("verb = %q, want mention", m.Verb)
	}
	if m.Sender != "operator" || m.Source != "cli" {
		t.Errorf("sender/source = %q/%q, want operator/cli", m.Sender, m.Source)
	}
}
