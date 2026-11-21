package objstore

import (
	"context"
	"errors"
	"testing"
)

var errLostAck = errors.New("lost acknowledgement")

type lostAckStore struct {
	marker map[string]string
	row    bool
}

func (s *lostAckStore) Query(_ context.Context, _ Query) ([]Item, error) {
	if s.marker == nil {
		return nil, nil
	}
	return []Item{{Attrs: s.marker}}, nil
}

func (s *lostAckStore) GuardedBatch(_ context.Context, checks []Check, writes []Write) (bool, error) {
	if len(checks) > 0 {
		s.row = true
		return false, errLostAck
	}
	w := writes[0]
	if s.marker == nil {
		s.marker = make(map[string]string)
	}
	for k, v := range w.Attrs {
		s.marker[k] = v
	}
	for _, k := range w.Remove {
		delete(s.marker, k)
	}
	return true, nil
}

func TestReplaceKeepsClaimAfterLostWriteAcknowledgement(t *testing.T) {
	s := &lostAckStore{}
	sc := Scope{Entity: "markers", Key: Key{Partition: "p", Sort: "s"}}
	_, err := Replace(t.Context(), s, sc, "", Claim{Intent: "first"}, func(ctx context.Context, r *Replacer) error {
		_, err := r.Write(ctx, Write{Op: Put, Entity: "rows", Key: Key{Partition: "p", Sort: "r"}})
		return err
	})
	if !errors.Is(err, errLostAck) || !s.row {
		t.Fatalf("write = %v, committed row = %v", err, s.row)
	}
	_, err = Replace(t.Context(), s, sc, "", Claim{Intent: "different"}, func(context.Context, *Replacer) error {
		t.Fatal("different work entered a partially written scope")
		return nil
	})
	if !errors.Is(err, ErrScopeHeld) {
		t.Fatalf("different work = %v, want ErrScopeHeld", err)
	}
}
