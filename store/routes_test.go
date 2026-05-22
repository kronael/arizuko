package store

import (
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestAddRoute_RejectsWebJIDPredicate(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"exact", "chat_jid=web:atlas/strengths"},
		{"glob", "chat_jid=web:atlas/strengths/*"},
		{"with-platform", "platform=web chat_jid=web:atlas"},
		{"multi-token", "verb=mention chat_jid=web:foo/bar"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openMem(t)
			_, err := s.AddRoute(core.Route{Match: c.match, Target: "atlas/strengths"})
			if !errors.Is(err, ErrWebJIDRouted) {
				t.Errorf("AddRoute(%q) err = %v, want ErrWebJIDRouted", c.match, err)
			}
		})
	}
}

func TestAddRoute_AllowsNonWebJID(t *testing.T) {
	cases := []string{
		"chat_jid=telegram:123",
		"chat_jid=slack:C0123",
		"chat_jid=discord:guild/general",
		"platform=telegram room=123",
		"chat_jid=slink:9f31507f1d5c8017",
		"chat_jid=hook:atlas/strengths/intake",
		"", // empty match — wildcard
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			s := openMem(t)
			if _, err := s.AddRoute(core.Route{Match: m, Target: "alpha"}); err != nil {
				t.Errorf("AddRoute(%q) err = %v, want nil", m, err)
			}
		})
	}
}
