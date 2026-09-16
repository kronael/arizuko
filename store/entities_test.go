package store

import (
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// openEntities opens a store holding BOTH owners' entities and the audit
// record — what a real opener makes at boot, through the one applier
// (store.PrepareOwner, reached from routd/db.go and onbod/db.go). A real opener
// names the single owner whose tables it holds; this stand-in stands in for
// every one of them, so it asks for both.
func openEntities(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("open a test namespace: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("release the test namespace: %v", err)
		}
	})
	return s
}
