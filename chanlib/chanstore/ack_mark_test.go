package chanstore_test

// `ack_marks` bodies: the record that lets an adapter find, after a restart,
// the message its acknowledgement mark stands on.

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
)

func TestAckMarkRoundTrips(t *testing.T) {
	st := newStore(t)
	at := ts(time.Now())
	if err := st.SetMark(t.Context(), "github:o/r/pr/1", "gh-c42", "9001", at); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, held, err := st.MarkFor(t.Context(), "github:o/r/pr/1")
	if err != nil || !held {
		t.Fatalf("read back = (%+v, %v, %v)", got, held, err)
	}
	want := chanstore.Mark{ChatJID: "github:o/r/pr/1", Target: "gh-c42", Handle: "9001", SetAt: at}
	if got != want {
		t.Errorf("mark = %+v, want %+v", got, want)
	}
}

// A platform that removes by coordinate has no handle; the item carries none.
func TestAckMarkWithoutHandle(t *testing.T) {
	st := newStore(t)
	if err := st.SetMark(t.Context(), "slack:T/channel/C1", "slack:T/channel/C1/1.2", "", ts(time.Now())); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, held, err := st.MarkFor(t.Context(), "slack:T/channel/C1")
	if err != nil || !held || got.Handle != "" {
		t.Fatalf("read back = (%+v, %v, %v)", got, held, err)
	}
}

// A chat has one mark: a second SetMark replaces the first.
func TestAckMarkSetReplaces(t *testing.T) {
	st := newStore(t)
	for _, target := range []string{"first", "second"} {
		if err := st.SetMark(t.Context(), "j", target, "h-"+target, ts(time.Now())); err != nil {
			t.Fatalf("set %s: %v", target, err)
		}
	}
	marks, err := st.Marks(t.Context())
	if err != nil || len(marks) != 1 || marks[0].Target != "second" || marks[0].Handle != "h-second" {
		t.Fatalf("marks = (%+v, %v), want one mark on second", marks, err)
	}
}

func TestAckMarkMissAndDrop(t *testing.T) {
	st := newStore(t)
	if _, held, err := st.MarkFor(t.Context(), "nobody"); err != nil || held {
		t.Fatalf("a chat with no mark = (held %v, %v), want a plain miss", held, err)
	}
	if err := st.SetMark(t.Context(), "a", "t", "", ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMark(t.Context(), "b", "t", "", ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := st.DropMark(t.Context(), "a"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := st.DropMark(t.Context(), "a"); err != nil {
		t.Fatalf("dropping a dropped mark must be a no-op: %v", err)
	}
	marks, err := st.Marks(t.Context())
	if err != nil || len(marks) != 1 || marks[0].ChatJID != "b" {
		t.Fatalf("marks = (%+v, %v), want only b", marks, err)
	}
}

func TestAckMarkIsDeclared(t *testing.T) {
	for _, e := range chanstore.Entities {
		if e == chanstore.AckMark {
			return
		}
	}
	t.Fatalf("chanstore.Entities does not declare %q, so no adapter would have the table",
		chanstore.AckMark)
}
