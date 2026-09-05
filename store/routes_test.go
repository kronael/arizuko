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

func TestUpdateRoute_RejectsWebJIDPredicate(t *testing.T) {
	s := openMem(t)
	id, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "alpha"})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	_, err = s.UpdateRoute(id, core.Route{Match: "chat_jid=web:alpha", Target: "alpha"})
	if !errors.Is(err, ErrWebJIDRouted) {
		t.Errorf("UpdateRoute(web jid) err = %v, want ErrWebJIDRouted", err)
	}
}

// An update that matches no row is not a mutation: no rows, no audit row.
func TestUpdateRoute_MissingIDRecordsNothing(t *testing.T) {
	s := openMem(t)
	n, err := s.UpdateRoute(404, core.Route{Match: "chat_jid=telegram:1", Target: "alpha"})
	if err != nil || n != 0 {
		t.Fatalf("UpdateRoute(missing) = %d, %v; want 0, nil", n, err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='route.update'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("audit rows for an update that matched nothing: %d", rows)
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
