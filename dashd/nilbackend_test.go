package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// When routd.db fails to open, adminDB() is nil. Every read handler must
// degrade to a banner / 503 rather than panic on a nil *sql.DB deref.
func TestReadHandlers_nilAdminDB(t *testing.T) {
	d := &dash{groupsDir: t.TempDir()} // all DB handles nil
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	for _, path := range []string{
		"/dash/",
		"/dash/groups/",
		"/dash/tasks/",
		"/dash/tasks/x/list",
		"/dash/tasks/t-1", // detail
		"/dash/activity/",
		"/dash/activity/x/recent",
		"/dash/status/",
		"/dash/memory/",
		"/dash/chat/",
		"/dash/chat/eng/", // per-folder, gated by requireVisible
		"/dash/tokens/eng/",
		"/dash/profile/",
	} {
		w := httptest.NewRecorder()
		// asOperator so callerScope resolves without the (nil) adminDB fallback;
		// ServeHTTP panicking here would fail the test.
		mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", path, nil)))
		if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 200 or 503 (no panic)", path, w.Code)
		}
	}
}
