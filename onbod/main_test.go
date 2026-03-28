package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE routes (jid TEXT, seq INTEGER, type TEXT, match TEXT, target TEXT);
		CREATE TABLE registered_groups (folder TEXT PRIMARY KEY, parent TEXT, jid TEXT);
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, world_name TEXT, prompted_at TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNameREValid(t *testing.T) {
	valid := []string{"hello", "my-workspace", "abc123", "a", "test-1-2-3", "123", "-hyphened"}
	for _, v := range valid {
		if !nameRE.MatchString(v) {
			t.Errorf("nameRE rejected valid name %q", v)
		}
	}
}

func TestNameREInvalid(t *testing.T) {
	invalid := []string{"Hello", "my workspace", "abc!", "ABC", "", "has/slash", "has.dot", "CamelCase"}
	for _, v := range invalid {
		if nameRE.MatchString(v) {
			t.Errorf("nameRE accepted invalid name %q", v)
		}
	}
}

func TestIsTier0True(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO registered_groups (folder, parent) VALUES ('main', NULL)`)
	db.Exec(`INSERT INTO routes (jid, seq, type, target) VALUES ('telegram:123', 0, 'default', 'main')`)
	if !isTier0(db, "telegram:123") {
		t.Error("expected isTier0=true for root group sender")
	}
}

func TestIsTier0False(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO registered_groups (folder, parent) VALUES ('sub', 'main')`)
	db.Exec(`INSERT INTO routes (jid, seq, type, target) VALUES ('telegram:456', 0, 'default', 'sub')`)
	if isTier0(db, "telegram:456") {
		t.Error("expected isTier0=false for child group sender")
	}
}

func TestIsTier0NotFound(t *testing.T) {
	db := testDB(t)
	if isTier0(db, "telegram:999") {
		t.Error("expected isTier0=false for unknown sender")
	}
}

func TestHandleSendMalformedJSON(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleSendBadAuth(t *testing.T) {
	db := testDB(t)
	cfg := config{secret: "sekret"}
	body, _ := json.Marshal(map[string]string{"jid": "x", "text": "/approve y"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestHandleSendUnknownCommand(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleSendApproveNotTier0(t *testing.T) {
	db := testDB(t)
	cfg := config{} // empty gatedURL: sendReply fails silently, 403 still written
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "/approve somename"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}
