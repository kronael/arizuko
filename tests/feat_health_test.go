// Health endpoint tests: importable daemons (routd, runed) are booted via
// bootFederation and their /health probed. Package-main daemons (authd,
// dashd, timed) have their own *_test.go; smoke tests check all live.
package tests

import (
	"net/http"
	"testing"
)

func TestFeature_HealthEndpoints(t *testing.T) {
	t.Run("routd-health", func(t *testing.T) {
		f := bootFederation(t)
		assertHealth(t, f.routdTS.URL)
	})

	t.Run("runed-health", func(t *testing.T) {
		f := bootFederation(t)
		assertHealth(t, f.runedTS.URL)
	})

	// routd /health is mounted before auth middleware — no token needed.
	t.Run("routd-health-no-auth", func(t *testing.T) {
		f := bootFederation(t)
		resp, err := http.Get(f.routdTS.URL + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("/health without token = %d, want 200", resp.StatusCode)
		}
	})
}

func assertHealth(t *testing.T, base string) {
	t.Helper()
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/health = %d, want 200", resp.StatusCode)
	}
}
