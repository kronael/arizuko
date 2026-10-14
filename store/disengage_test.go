package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestEngagementRenewalPreservesExplicitDisengage(t *testing.T) {
	s := openMem(t)
	t.Cleanup(func() { s.Close() })
	const jid = "slack:T/C"
	const folder = "eng"
	until := time.Now().Add(time.Hour)
	if _, err := s.OpenTurnContext("old", folder, "one", jid, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpEngagement(jid, "one", folder, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{jid, "one"}, {jid, "two"}, {"slack:T/OTHER", "one"}} {
		if err := s.RenewEngagement("old", pair[0], pair[1], folder, until); err != nil {
			t.Fatal(err)
		}
		if _, live := s.Engagement(pair[0], pair[1]); live {
			t.Fatalf("old turn reopened %v", pair)
		}
	}
	if _, err := s.OpenTurnContext("other-topic", folder, "two", jid, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewEngagement("other-topic", jid, "two", folder, until); err != nil {
		t.Fatal(err)
	}
	if _, live := s.Engagement(jid, "two"); !live {
		t.Fatal("disengage leaked into another topic")
	}
	if _, err := s.OpenTurnContext("other-jid", folder, "one", "slack:T/OTHER", "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewEngagement("other-jid", "slack:T/OTHER", "one", folder, until); err != nil {
		t.Fatal(err)
	}
	if _, live := s.Engagement("slack:T/OTHER", "one"); !live {
		t.Fatal("disengage leaked into another chat")
	}
	if err := s.BumpEngagement(jid, "one", folder, until); err != nil {
		t.Fatal(err)
	}
	if _, live := s.Engagement(jid, "one"); !live {
		t.Fatal("explicit engage failed")
	}
	if _, err := s.OpenTurnContext("timed", folder, "idle", jid, "timed-isolated:task", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewEngagement("timed", jid, "idle", folder, until); err != nil {
		t.Fatal(err)
	}
	if _, live := s.Engagement(jid, "idle"); live {
		t.Fatal("timed turn engaged an idle conversation")
	}
}

type disengageBeforeRenewal struct {
	objstore.Store
	jid     string
	topic   string
	folder  string
	cleared bool
}

func (s *disengageBeforeRenewal) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !s.cleared {
		for _, w := range writes {
			if w.Entity == ChatReplyState && w.Attrs[AttrReplyEngagedUntil] != "" {
				s.cleared = true
				if err := NewOver(s.Store).BumpEngagement(s.jid, s.topic, s.folder, time.Time{}); err != nil {
					return false, err
				}
				break
			}
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestEngagementRenewalLosesToConcurrentDisengage(t *testing.T) {
	for _, test := range []struct {
		destination string
		cleared     bool
	}{{"root", false}, {"new-thread", false}, {"root", true}, {"new-thread", true}} {
		t.Run(test.destination, func(t *testing.T) {
			destination := test.destination
			s := openMem(t)
			t.Cleanup(func() { s.Close() })
			const jid = "slack:T/C"
			const folder = "eng"
			if test.cleared {
				if err := s.BumpEngagement(jid, "root", folder, time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.OpenTurnContext("turn", folder, "root", jid, "user", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.SetLastReply(jid, destination, "reply", folder); err != nil {
				t.Fatal(err)
			}
			concurrent := &disengageBeforeRenewal{Store: s.Obj(), jid: jid, topic: "root", folder: folder}
			if err := NewOver(concurrent).RenewEngagement("turn", jid, destination, folder, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if !concurrent.cleared {
				t.Fatal("concurrent clear did not run")
			}
			if _, live := s.Engagement(jid, destination); live {
				t.Fatal("renewal overwrote concurrent disengage")
			}
		})
	}
}

func TestFreshEngagementRetiresDisengageAfterExpiry(t *testing.T) {
	for _, topic := range []string{"", "thread"} {
		for _, mode := range []string{"explicit", "renewal"} {
			t.Run(topic+"/"+mode, func(t *testing.T) {
				s := openMem(t)
				t.Cleanup(func() { s.Close() })
				const jid = "slack:T/C"
				const folder = "eng"
				if err := s.BumpEngagement(jid, topic, folder, time.Time{}); err != nil {
					t.Fatal(err)
				}
				until := time.Now().Add(time.Hour)
				if mode == "explicit" {
					if err := s.BumpEngagement(jid, topic, folder, until); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.OpenTurnContext("fresh", folder, topic, jid, "user", ""); err != nil {
						t.Fatal(err)
					}
					if err := s.RenewEngagement("fresh", jid, topic, folder, until); err != nil {
						t.Fatal(err)
					}
				}
				key, err := replyStateKey(jid, topic)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: ChatReplyState, Key: key, Attrs: map[string]string{AttrReplyEngagedUntil: core.TS(time.Now().Add(-time.Second))}}); err != nil {
					t.Fatal(err)
				}
				if _, live := s.Engagement(jid, topic); live {
					t.Fatal("engagement did not expire")
				}
				checkTopic := topic
				if checkTopic == "" {
					checkTopic = "child-thread"
				}
				if stopped, err := s.ThreadDisengaged(jid, checkTopic); err != nil || stopped {
					t.Fatalf("fresh engagement retained old stop after expiry: stopped=%v err=%v", stopped, err)
				}
			})
		}
	}
}
