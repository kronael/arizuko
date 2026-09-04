package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestTaskRows_WindowAppliesAfterTheVisibilityFilter is the bug the old SQL
// pushed the scope into its WHERE to avoid: with a plain `LIMIT 500` applied
// BEFORE the owner filter, a scoped user's task vanishes whenever 500
// other-owner rows sort ahead of it.
//
// Federating the read moved the filter into Go, and the safety now rests on two
// facts rather than on SQL: routd's list is UNCAPPED (dashd's service token
// carries an empty folder claim, which routd reads as list-all), and the display
// window counts RENDERED rows. Truncating the fetch — at either end — puts the
// bug straight back, which is what this test fails on.
func TestTaskRows_WindowAppliesAfterTheVisibilityFilter(t *testing.T) {
	db := routdDB(t)
	defer db.Close()

	// taskPageSize other-owner rows that sort ahead of the caller's one task.
	for i := 0; i < taskPageSize+50; i++ {
		if _, err := db.Exec(
			`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, status, created_at)
			 VALUES(?, 'aaa-other', 'web:aaa-other', 'noise', '* * * * *', 'active', '')`,
			fmt.Sprintf("noise-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES('zzz-mine', 'zzz-mine', 'web:zzz-mine', 'the caller''s own task', '* * * * *', 'active', '')`); err != nil {
		t.Fatal(err)
	}

	d := &dash{dbRoutd: db, routdURL: stubRoutd(t, db)}
	var w strings.Builder
	d.writeTaskRows(t.Context(), &stringResponse{&w}, []string{"zzz-mine"}, false)

	if !strings.Contains(w.String(), "zzz-mine") {
		t.Error("the caller's own task was dropped: the fetch must stay uncapped " +
			"and the window must count rendered rows, or a scoped user loses " +
			"tasks behind other owners' noise")
	}
	if strings.Contains(w.String(), "aaa-other") {
		t.Error("another owner's task leaked to a scoped caller")
	}
}

// TestTaskRows_RenderThroughRoutd — the plain path: an operator sees the tasks
// routd serves. Without this, a reader that returns nothing renders as "no
// scheduled tasks" and no test notices.
func TestTaskRows_RenderThroughRoutd(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES('t1', 'alpha', 'web:alpha', 'water the plants', '0 8 * * *', 'active', '')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{dbRoutd: db, routdURL: stubRoutd(t, db)}
	var w strings.Builder
	d.writeTaskRows(t.Context(), &stringResponse{&w}, nil, true)

	for _, want := range []string{"t1", "alpha", "water the plants"} {
		if !strings.Contains(w.String(), want) {
			t.Errorf("missing %q in the rendered rows:\n%s", want, w.String())
		}
	}
	if strings.Contains(w.String(), "No scheduled tasks") {
		t.Error("rows exist in routd but the page rendered the empty state")
	}
}

// stringResponse adapts a strings.Builder to http.ResponseWriter for the
// fragment writers, which only ever Write.
type stringResponse struct{ b *strings.Builder }

func (s *stringResponse) Header() http.Header         { return http.Header{} }
func (s *stringResponse) Write(p []byte) (int, error) { return s.b.Write(p) }
func (s *stringResponse) WriteHeader(int)             {}
