package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashReads_RefuseWithoutScope — these are aggregates over another folder's
// data, so an unscoped caller must be refused. They are plain handlers rather
// than resreg resources precisely so they do NOT become agent tools; that makes
// the scope check the only gate, and an untested gate is no gate.
func TestDashReads_RefuseWithoutScope(t *testing.T) {
	db := memDBOrSkip(t)
	srv := &Server{db: db, verify: fakeVerifier{sub: "user:nobody", scope: []string{"audit:read"}}}
	for _, path := range []string{"/v1/sessions/counts", "/v1/messages/volume"} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /v1/sessions/counts", srv.handleSessionCounts)
		mux.HandleFunc("GET /v1/messages/volume", srv.handleMessageVolume)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s served a caller holding only audit:read", path)
		}
	}
}

// TestMessageVolume_CountsHumansOnly — the is_bot_message filter lives with the
// OWNER, so a caller cannot widen the histogram to include the agent's own
// output. That is the reason the aggregate is served rather than the rows.
func TestMessageVolume_CountsHumansOnly(t *testing.T) {
	db := memDBOrSkip(t)
	if _, err := db.SQL().Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp, is_bot_message)
		 VALUES('h1','web:a','u','hi',datetime('now'),0),
		       ('b1','web:a','bot','hello',datetime('now'),1)`); err != nil {
		t.Fatal(err)
	}
	srv := &Server{db: db}
	w := httptest.NewRecorder()
	srv.handleMessageVolume(w, httptest.NewRequest("GET", "/v1/messages/volume", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var days []dailyVolume
	if err := json.Unmarshal(w.Body.Bytes(), &days); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range days {
		total += d.Count
	}
	if total != 1 {
		t.Errorf("counted %d messages, want 1 — the bot row must not be counted", total)
	}
}

func memDBOrSkip(t *testing.T) *DB {
	t.Helper()
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
