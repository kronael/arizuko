package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type pollDB struct{ rows map[string]map[string]string }

func (d *pollDB) Query(_ context.Context, q objstore.Query) ([]objstore.Item, error) {
	row := d.rows[q.Key.Value]
	if row == nil {
		return nil, nil
	}
	return []objstore.Item{{Attrs: row}}, nil
}
func (d *pollDB) GuardedBatch(_ context.Context, _ []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		row := map[string]string{}
		for k, v := range w.Attrs {
			row[k] = v
		}
		d.rows[w.Key.Sort] = row
	}
	return true, nil
}

type pollBindings []store.GDriveFile

func (b pollBindings) ListGDriveFiles(string) ([]store.GDriveFile, error) { return b, nil }

type pollRouter struct {
	messages []chanlib.InboundMsg
	fail     bool
}

func (r *pollRouter) SendMessage(m chanlib.InboundMsg) error {
	if r.fail {
		return fmt.Errorf("refused")
	}
	r.messages = append(r.messages, m)
	return nil
}
func newPollTest(t *testing.T, handler http.HandlerFunc, files ...string) (*poller, *pollDB, *pollRouter) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client(), state: credOK, user: driveUser{EmailAddress: "agent@example.com"}}}
	bindings := pollBindings{}
	for _, id := range files {
		bindings = append(bindings, store.GDriveFile{FileID: id})
	}
	db := &pollDB{rows: map[string]map[string]string{}}
	router := &pollRouter{}
	return &poller{b: b, bindings: bindings, db: db, router: router, name: "gdrive", interval: 30 * time.Second, files: map[string]*filePoll{}}, db, router
}

const thread = `{"id":"c","createdTime":"2026-01-01T00:00:00Z","modifiedTime":"2026-01-02T00:00:00Z","content":"hello","author":{"displayName":"Alice"},"replies":[{"id":"r","createdTime":"2026-01-01T00:01:00Z","content":"reply","author":{"displayName":"Bob"}}]}`

func TestPollPagingAndCursor(t *testing.T) {
	calls := 0
	p, db, router := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprint(w, `{"name":"Document"}`)
			return
		}
		calls++
		q := r.URL.Query()
		if q.Get("fields") != commentFields || q.Get("pageSize") != "100" {
			t.Errorf("query: %v", q)
		}
		if calls == 1 {
			fmt.Fprintf(w, `{"nextPageToken":"next","comments":[%s]}`, thread)
			return
		}
		if q.Get("pageToken") != "next" {
			t.Errorf("page token: %v", q)
		}
		fmt.Fprint(w, `{"comments":[]}`)
	}, "file")
	p.sweep(t.Context(), time.Now())
	if calls != 2 || len(router.messages) != 2 || router.messages[0].ChatName != "Document" || p.b.LastInboundAt() == 0 {
		t.Fatalf("calls=%d messages=%+v inbound=%d", calls, router.messages, p.b.LastInboundAt())
	}
	if got := db.rows["file"]["cursor"]; got != "2026-01-02T00:00:00Z" {
		t.Fatal(got)
	}
}
func TestPollRefusedDeliveryKeepsCursor(t *testing.T) {
	p, db, router := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprintf(w, `{"comments":[%s]}`, thread)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}, "file")
	router.fail = true
	p.sweep(t.Context(), time.Now())
	if db.rows["file"]["cursor"] != "" {
		t.Fatal(db.rows)
	}
	router.fail = false
	p.sweep(t.Context(), time.Now())
	if db.rows["file"]["cursor"] == "" || router.messages[0].ChatName != "file" {
		t.Fatal(db.rows, router.messages)
	}
}

func TestPollLaterPageFailureKeepsCursor(t *testing.T) {
	var starts []string
	fail := true
	p, db, router := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprint(w, `{}`)
			return
		}
		starts = append(starts, r.URL.Query().Get("startModifiedTime"))
		if r.URL.Query().Get("pageToken") == "" {
			fmt.Fprintf(w, `{"nextPageToken":"next","comments":[%s]}`, thread)
			return
		}
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"comments":[{"id":"older","createdTime":"2026-01-01T00:00:00Z","modifiedTime":"2026-01-01T12:00:00Z","content":"older"}]}`)
	}, "file")
	db.rows["file"] = map[string]string{"cursor": "2026-01-01T00:00:00Z"}
	p.sweep(t.Context(), time.Now())
	if got := db.rows["file"]["cursor"]; got != "2026-01-01T00:00:00Z" {
		t.Fatalf("cursor after failure = %q", got)
	}
	fail = false
	p.sweep(t.Context(), time.Now())
	if len(starts) != 4 || starts[2] != "2026-01-01T00:00:00Z" || starts[3] != starts[2] {
		t.Fatalf("start times = %v", starts)
	}
	if got := db.rows["file"]["cursor"]; got != "2026-01-02T00:00:00Z" || len(router.messages) != 5 {
		t.Fatalf("cursor = %q, messages = %d", got, len(router.messages))
	}
}

func TestPollWithoutAccount(t *testing.T) {
	p, db, router := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprintf(w, `{"comments":[%s]}`, thread)
			return
		}
		fmt.Fprint(w, `{}`)
	}, "file")
	p.b.cred.user = driveUser{}
	p.b.cred.state = credOK
	p.sweep(t.Context(), time.Now())
	if len(router.messages) != 2 || router.messages[0].Verb != "message" || db.rows["file"]["cursor"] == "" {
		t.Fatalf("messages = %+v, rows = %v", router.messages, db.rows)
	}
}
func TestPollBackoffAndFileIsolation(t *testing.T) {
	count := map[string]int{}
	status := 429
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprint(w, `{}`)
			return
		}
		id := strings.Split(r.URL.Path, "/")[4]
		count[id]++
		if id == "bad" {
			w.WriteHeader(status)
			if status == 403 {
				fmt.Fprint(w, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`)
			}
			return
		}
		fmt.Fprint(w, `{"comments":[]}`)
	}, "bad", "good")
	now := time.Now()
	p.sweep(t.Context(), now)
	if count["bad"] != 1 || count["good"] != 1 || db.rows["bad"]["status"] != "429" {
		t.Fatal(count, db.rows)
	}
	p.sweep(t.Context(), now.Add(29*time.Second))
	if count["bad"] != 1 || count["good"] != 2 {
		t.Fatal(count)
	}
	status = 403
	p.sweep(t.Context(), now.Add(30*time.Second))
	if p.files["bad"].delay != time.Minute {
		t.Fatal(p.files["bad"].delay)
	}
	status = 500
	for i := 0; i < 8; i++ {
		now = p.files["bad"].next
		p.sweep(t.Context(), now)
	}
	if p.files["bad"].delay != 15*time.Minute {
		t.Fatal(p.files["bad"].delay)
	}
	status = 404
	p.sweep(t.Context(), p.files["bad"].next)
	if db.rows["bad"]["status"] != "404" || count["good"] < 3 {
		t.Fatal(count, db.rows)
	}
}
func TestPollInterval(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 30 * time.Second}, {"1s", 10 * time.Second}, {"45s", 45 * time.Second}} {
		t.Setenv("GDRIVE_POLL_INTERVAL", tc.value)
		if got := pollInterval(); got != tc.want {
			t.Fatalf("%q: %v", tc.value, got)
		}
	}
}

func TestPollRestoresCursor(t *testing.T) {
	var cursor string
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			cursor = r.URL.Query().Get("startModifiedTime")
			fmt.Fprint(w, `{"comments":[]}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}, "file")
	db.rows["file"] = map[string]string{"cursor": "2026-01-02T00:00:00Z"}
	p.sweep(t.Context(), time.Now())
	if cursor != "2026-01-02T00:00:00Z" {
		t.Fatalf("startModifiedTime = %q", cursor)
	}
}
