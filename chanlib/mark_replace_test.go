package chanlib

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

func TestAckMark_FailedReplacementKeepsRecoveryRecord(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)
	inbound(t, rc, "m1", "mention")
	m.Typing(markJID, true)
	p.removeErr = errors.New("platform unavailable")
	inbound(t, rc, "m2", "mention")
	m.Typing(markJID, true)
	mark, held, err := st.MarkFor(t.Context(), markJID)
	if err != nil {
		t.Fatal(err)
	}
	if !held || mark.Target != "m1" {
		t.Errorf("recovery record = %+v, held=%v; want m1", mark, held)
	}
	if got := p.targets(); len(got) != 1 || got[0] != "m1" {
		t.Errorf("marks after failed replacement: %v; want only m1", got)
	}
	p.removeErr = nil
	m.Typing(markJID, false)
	if got := p.targets(); len(got) != 0 {
		t.Errorf("marks survived successful cleanup: %v", got)
	}
}

func TestAckMark_StoreFailurePreventsReplacement(t *testing.T) {
	for _, failure := range []string{"read", "delete"} {
		t.Run(failure, func(t *testing.T) {
			st := testStore(t)
			p := newPlatform()
			m, rc := newMark(t, st, p)
			inbound(t, rc, "m1", "mention")
			m.Typing(markJID, true)
			driver, err := dynstore.FromEnv(dynstore.Prefix("testadapter"))
			if err != nil {
				t.Fatal(err)
			}
			store := &markFailureStore{Store: driver}
			if failure == "read" {
				store.queryErr = errStoreRefused
			} else {
				store.deleteErr = errStoreRefused
			}
			m.st = chanstore.New(store)
			inbound(t, rc, "m2", "mention")
			m.Typing(markJID, true)
			mark, held, err := st.MarkFor(t.Context(), markJID)
			if err != nil {
				t.Fatal(err)
			}
			if !held || mark.Target != "m1" {
				t.Errorf("recovery record = %+v, held=%v; want m1", mark, held)
			}
			want := "add m1"
			if failure == "delete" {
				want += "; remove m1 h1"
			}
			if got := p.seen(); got != want {
				t.Errorf("calls = %q, want %q", got, want)
			}
			m.st = st
			m.Typing(markJID, false)
			if got := p.targets(); len(got) != 0 {
				t.Errorf("marks survived successful cleanup: %v", got)
			}
			if _, held, err := st.MarkFor(t.Context(), markJID); err != nil || held {
				t.Errorf("record after successful cleanup: held=%v, err=%v", held, err)
			}
		})
	}
}

type markFailureStore struct {
	objstore.Store
	queryErr  error
	deleteErr error
	putErr    error
}

func (s *markFailureStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	return s.Store.Query(ctx, q)
}

func (s *markFailureStore) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Op == objstore.Delete && s.deleteErr != nil {
			return false, s.deleteErr
		}
		if write.Op == objstore.Put && s.putErr != nil {
			return false, s.putErr
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}
