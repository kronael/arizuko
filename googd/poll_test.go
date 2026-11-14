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
	"github.com/kronael/arizuko/googd/googstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

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

type pollStateDB struct {
	objstore.Store
	t *testing.T
}

func (d *pollStateDB) item(id string) objstore.Item {
	d.t.Helper()
	rows, err := d.Query(d.t.Context(), objstore.Query{Entity: googstore.PollEntity, Partition: "gdrive", Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: id}})
	if err != nil || len(rows) != 1 {
		d.t.Fatalf("poll row %s: %v %v", id, rows, err)
	}
	return rows[0]
}
func (d *pollStateDB) row(id string) map[string]string { return d.item(id).Attrs }
func (d *pollStateDB) state(id string) *filePoll {
	d.t.Helper()
	s, err := pollState(d.item(id))
	if err != nil {
		d.t.Fatal(err)
	}
	return s
}
func (d *pollStateDB) put(id string, attrs map[string]string) {
	d.t.Helper()
	if ok, err := d.GuardedBatch(d.t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: googstore.PollEntity, Key: objstore.Key{Partition: "gdrive", Sort: id}, Attrs: attrs}}); err != nil || !ok {
		d.t.Fatalf("seed poll: %v %v", ok, err)
	}
}

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
func newPollTest(t *testing.T, handler http.HandlerFunc, files ...string) (*poller, *pollStateDB, *pollRouter) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client(), state: credOK, user: driveUser{EmailAddress: "agent@example.com"}}}
	raw, closeStore, err := store.TestOwner(store.OwnerRoutd, googstore.PollEntity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeStore(); err != nil {
			t.Error(err)
		}
	})
	bindings := store.NewOver(raw)
	for _, id := range files {
		if err := bindings.AddGDriveFile(store.GDriveFile{FileID: id, OwnerFolder: "world", TargetFolder: "world"}); err != nil {
			t.Fatal(err)
		}
	}
	indexed, err := googstore.Open(t.Context(), raw, "gdrive")
	if err != nil {
		t.Fatal(err)
	}
	db := &pollStateDB{Store: indexed, t: t}
	router := &pollRouter{}
	return &poller{b: b, bindings: bindings, db: indexed, router: router, name: "gdrive", interval: 30 * time.Second}, db, router
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
	if got := db.row("file")["cursor"]; got != "2026-01-02T00:00:00Z" {
		t.Fatal(got)
	}
}
func TestPollActionDeliveryAndCursor(t *testing.T) {
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Query().Get("pageToken") == "" {
			fmt.Fprint(w, `{"nextPageToken":"next","comments":[{"id":"c1","createdTime":"2026-01-01T00:00:00Z","modifiedTime":"2026-01-02T00:00:00Z","action":"resolve","author":{"displayName":"Alice"}}]}`)
			return
		}
		fmt.Fprint(w, `{"comments":[{"id":"c2","createdTime":"2026-01-01T00:00:00Z","modifiedTime":"2026-01-03T00:00:00Z","action":"reopen","author":{"displayName":"Alice"}}]}`)
	}, "file")
	ingressDB := &pollDB{rows: map[string]map[string]string{}}
	ingress := httptest.NewServer(routd.NewServer(routd.OpenOver(ingressDB), nil, nil, nil, 0, "").Handler())
	defer ingress.Close()
	p.router = chanlib.NewRouterClient(ingress.URL)
	p.sweep(t.Context(), time.Now())
	if got := db.row("file")["cursor"]; got != "2026-01-03T00:00:00Z" {
		t.Fatalf("cursor = %q, ingress rows = %v", got, ingressDB.rows)
	}
	for _, action := range []string{"resolve", "reopen"} {
		found := false
		for _, row := range ingressDB.rows {
			if row["verb"] == action && row["preview"] == "Comment "+map[string]string{"resolve": "resolved", "reopen": "reopened"}[action] {
				found = true
			}
		}
		if !found {
			t.Errorf("missing delivered %s: %v", action, ingressDB.rows)
		}
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
	if db.row("file")["cursor"] != "" {
		t.Fatal(db.row("file"))
	}
	router.fail = false
	p.sweep(t.Context(), db.state("file").next)
	if db.row("file")["cursor"] == "" || router.messages[0].ChatName != "file" {
		t.Fatal(db.row("file"), router.messages)
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
	db.put("file", map[string]string{"cursor": "2026-01-01T00:00:00Z"})
	p.sweep(t.Context(), time.Now())
	if got := db.row("file")["cursor"]; got != "2026-01-01T00:00:00Z" {
		t.Fatalf("cursor after failure = %q", got)
	}
	fail = false
	p.sweep(t.Context(), db.state("file").next)
	if len(starts) != 4 || starts[2] != "2026-01-01T00:00:00Z" || starts[3] != starts[2] {
		t.Fatalf("start times = %v", starts)
	}
	if got := db.row("file")["cursor"]; got != "2026-01-02T00:00:00Z" || len(router.messages) != 5 {
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
	if len(router.messages) != 2 || router.messages[0].Verb != "message" || db.row("file")["cursor"] == "" {
		t.Fatalf("messages = %+v, rows = %v", router.messages, db.row("file"))
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
	if count["bad"] != 1 || count["good"] != 1 || db.row("bad")["status"] != "429" {
		t.Fatal(count, db.row("bad"))
	}
	p.sweep(t.Context(), now.Add(29*time.Second))
	if count["bad"] != 1 || count["good"] != 1 {
		t.Fatal(count)
	}
	status = 403
	p.sweep(t.Context(), db.state("bad").next)
	if db.state("bad").delay != time.Minute {
		t.Fatal(db.state("bad").delay)
	}
	status = 500
	for i := 0; i < 8; i++ {
		now = db.state("bad").next
		p.sweep(t.Context(), now)
	}
	if db.state("bad").delay != 15*time.Minute {
		t.Fatal(db.state("bad").delay)
	}
	status = 404
	p.sweep(t.Context(), db.state("bad").next)
	if db.row("bad")["status"] != "404" || count["good"] < 3 {
		t.Fatal(count, db.row("bad"))
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
	db.put("file", map[string]string{"cursor": "2026-01-02T00:00:00Z"})
	p.sweep(t.Context(), time.Now())
	if cursor != "2026-01-02T00:00:00Z" {
		t.Fatalf("startModifiedTime = %q", cursor)
	}
}

func TestDashboardDisplaysPersistedPollState(t *testing.T) {
	p, _, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			fmt.Fprintf(w, `{"comments":[%s]}`, thread)
			return
		}
		fmt.Fprint(w, `{"name":"Document"}`)
	}, "file")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	p.sweep(t.Context(), now)
	p.b.poll = p
	p.b.cred.driveAPI = apiState{state: "on"}
	p.b.cred.sheetsAPI = apiState{state: "off", reason: "Enable Sheets"}
	mux := chanlib.NewAdapterMux("googd", []string{"gdrive:"}, p.b, p.b.isConnected, p.b.LastInboundAt, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dash/googd/", nil))
	for _, want := range []string{"Drive", "Sheets", "Enable Sheets", "sheets.googleapis.com", "drive.googleapis.com", "file", "Document", "2026-11-07T12:00:00Z", "status: ok", "2026-01-02T00:00:00Z", "agent@example.com"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboardShowsFailedPollStatuses(t *testing.T) {
	for _, status := range []int{403, 404, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			p, _, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/comments") {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"name":"Document"}`)
			}, "file")
			p.sweep(t.Context(), time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
			section := p.dashSection()
			if len(section.Rows) != 1 || !strings.Contains(section.Rows[0][1], fmt.Sprintf("status: %d", status)) {
				t.Fatalf("%+v", section)
			}
		})
	}
}
