package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestRouteTopicUsesNativeThreadBeforeSticky(t *testing.T) {
	for _, tc := range []struct{ target, native, want string }{
		{"team", "", "sticky"},
		{"team", "native", "native"},
		{"team#{thread}", "native", "native"},
		{"team#{thread}", "", ""},
		{"team#prefix-{thread}", "native", "prefix-native"},
		{"team#prefix-{thread}", "", "prefix-"},
		{"team#{jid}", "native", "slack:channel"},
	} {
		t.Run(tc.target+"/"+tc.native, func(t *testing.T) {
			s := openMem(t)
			if err := s.SetStickyTopic("slack:channel", "sticky"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PutRouteRow(core.Route{Match: "platform=slack", Target: tc.target}); err != nil {
				t.Fatal(err)
			}
			m, err := s.AppendMessage(core.Message{ID: "in", ChatJID: "slack:channel", Sender: "user", Topic: tc.native, Timestamp: time.Now()})
			if err != nil || m.Topic != tc.want {
				t.Fatalf("topic = %q, error = %v; want %q", m.Topic, err, tc.want)
			}
		})
	}
}
