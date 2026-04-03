package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE registered_groups (
			jid TEXT PRIMARY KEY, name TEXT, folder TEXT, trigger_word TEXT,
			added_at TEXT, parent TEXT, state TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE sessions (group_folder TEXT PRIMARY KEY, session_id TEXT)`,
		`CREATE TABLE channels (name TEXT, url TEXT)`,
		`CREATE TABLE scheduled_tasks (
			id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT,
			cron TEXT, next_run TEXT, status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TEXT, source TEXT, group_folder TEXT, verb TEXT)`,
		`CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, channel TEXT,
			is_group INTEGER DEFAULT 0, last_message_time TEXT, errored INTEGER DEFAULT 0)`,
		`CREATE TABLE task_run_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT, run_at TEXT, duration_ms INTEGER, status TEXT, result TEXT, error TEXT)`,
		`CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, jid TEXT,
			seq INTEGER DEFAULT 0, type TEXT DEFAULT 'default', match TEXT, target TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func mintTestJWT(secret []byte, sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	c := fmt.Sprintf(`{"sub":%q,"name":"test","exp":%d,"iat":%d}`,
		sub, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
	body := base64.RawURLEncoding.EncodeToString([]byte(c))
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(hdr + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return hdr + "." + body + "." + sig
}

func TestDashHealth(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: nil}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("health = %v", resp)
	}
}

func TestDashRequireAuthNoSecret(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: nil}
	called := false
	h := d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if !called {
		t.Error("handler not called when no secret")
	}
}

func TestDashRequireAuthNoToken(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: []byte("secret"), webHost: "http://test"}
	called := false
	h := d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if called {
		t.Error("handler called despite missing token")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestDashRequireAuthBadToken(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	secret := []byte("secret")
	d := &dash{db: db, secret: secret, webHost: "http://test"}
	h := d.requireAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("GET", "/dash/", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestDashRequireAuthValidJWT(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	secret := []byte("secret")
	d := &dash{db: db, secret: secret}
	called := false
	h := d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	tok := mintTestJWT(secret, "user1")
	req := httptest.NewRequest("GET", "/dash/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, req)
	if !called {
		t.Error("handler not called with valid JWT")
	}
}

func TestDashPortal(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: nil}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" {
		t.Error("no Content-Type")
	}
}

func TestDashStatus(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: nil, dbPath: ":memory:"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestDashTasks(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, secret: nil}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/tasks/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}
