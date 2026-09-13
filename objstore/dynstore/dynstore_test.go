package dynstore_test

import (
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// newStore hands each test its own table namespace on the shared endpoint.
func newStore(t *testing.T, entities ...string) objstore.Store {
	return dyntest.Store(t, entities...)
}

func TestConformance(t *testing.T) { conformance.Run(t, newStore) }
