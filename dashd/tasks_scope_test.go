package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// writeTaskRows must scope the query for non-operators so the LIMIT counts only
// visible rows. A scoped caller's task must render even when 500 other-owner
// tasks sort ahead of it (owner order); the old plain LIMIT 500 cut it off.
func TestWriteTaskRows_visibleBeyondLimit(t *testing.T) {
	d, _, routd := splitAdminDash(t, "op@x")
	tx, err := routd.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		if _, err := tx.Exec(
			`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, status, created_at)
			 VALUES (?, ?, 'web:x', 'p', 'active', '')`,
			fmt.Sprintf("t-a%03d", i), fmt.Sprintf("aaa%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, status, created_at)
		 VALUES ('t-vis', 'zzz', 'web:zzz', 'p', 'active', '')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/dash/tasks/x/list", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["zzz"]`) // scoped, non-operator
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "t-vis") {
		t.Errorf("scoped task cut by LIMIT-before-filter")
	}
	if strings.Contains(body, "t-a000") {
		t.Errorf("other-owner task leaked to scoped viewer")
	}
}
