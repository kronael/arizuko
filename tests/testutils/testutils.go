// Package testutils provides shared fixtures for daemon integration tests:
// an in-memory DB wired to store migrations, a FakeChannel recording all
// outbound calls, a FakePlatform httptest server, and small assertion helpers.
package testutils

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/store"
	_ "modernc.org/sqlite"
)

// Inst is a minimal per-test fixture. Tmp is a temp dir. DB is an open,
// migrated in-memory sqlite handle. Store wraps DB. ChanReg is an empty
// registry whose HMAC secret matches JWTSecret.
type Inst struct {
	DB        *sql.DB
	Store     *store.Store
	Tmp       string
	JWTSecret []byte
	ChanReg   *chanreg.Registry
}

// NewInstance builds an Inst. Registers t.Cleanup to close the DB.
func NewInstance(t *testing.T) *Inst {
	t.Helper()
	tmp := t.TempDir()
	dsn := "file:" + filepath.Join(tmp, "test.db") + "?cache=shared&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		t.Fatalf("pragma: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	inst := &Inst{
		DB:        db,
		Store:     store.New(db),
		Tmp:       tmp,
		JWTSecret: secret,
		ChanReg:   chanreg.New(string(secret)),
	}
	t.Cleanup(func() { db.Close() })
	return inst
}

// SentMsg records a Send call on FakeChannel.
type SentMsg struct {
	JID, Text, ReplyTo, ThreadID string
}

// SentFile records a SendFile call.
type SentFile struct {
	JID, Path, Name, Caption string
}

// ReactionCall records a React call.
type ReactionCall struct {
	JID, TargetID, Reaction string
}

// PostCall records a Post call.
type PostCall struct {
	JID, Content string
	Media        []string
}

// DeleteCall records a DeletePost call.
type DeleteCall struct {
	JID, TargetID string
}

// FakeChannel is a thread-safe core.Channel + core.Socializer implementation
// that records every outbound interaction for later assertion.
type FakeChannel struct {
	ChannelName string
	Prefixes    []string

	mu           sync.Mutex
	SentMessages []SentMsg
	SentFiles    []SentFile
	Reactions    []ReactionCall
	Posts        []PostCall
	Deletes      []DeleteCall
	TypingCalls  int

	// SendErr, if non-nil, is returned from Send/SendFile.
	SendErr error
}

// NewFakeChannel returns a FakeChannel owning jids with the given prefixes.
func NewFakeChannel(name string, prefixes ...string) *FakeChannel {
	return &FakeChannel{ChannelName: name, Prefixes: prefixes}
}

func (f *FakeChannel) Name() string                   { return f.ChannelName }
func (f *FakeChannel) Connect(_ context.Context) error { return nil }
func (f *FakeChannel) Disconnect() error              { return nil }

func (f *FakeChannel) Owns(jid string) bool {
	for _, p := range f.Prefixes {
		if strings.HasPrefix(jid, p) {
			return true
		}
	}
	return false
}

func (f *FakeChannel) Send(jid, text, replyTo, threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendErr != nil {
		return "", f.SendErr
	}
	f.SentMessages = append(f.SentMessages, SentMsg{jid, text, replyTo, threadID})
	return fmt.Sprintf("fake-%d", len(f.SentMessages)), nil
}

func (f *FakeChannel) SendFile(jid, path, name, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendErr != nil {
		return f.SendErr
	}
	f.SentFiles = append(f.SentFiles, SentFile{jid, path, name, caption})
	return nil
}

func (f *FakeChannel) Typing(_ string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TypingCalls++
	return nil
}

func (f *FakeChannel) Post(_ context.Context, jid, content string, media []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Posts = append(f.Posts, PostCall{jid, content, media})
	return fmt.Sprintf("post-%d", len(f.Posts)), nil
}

func (f *FakeChannel) React(_ context.Context, jid, target, reaction string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reactions = append(f.Reactions, ReactionCall{jid, target, reaction})
	return nil
}

func (f *FakeChannel) DeletePost(_ context.Context, jid, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deletes = append(f.Deletes, DeleteCall{jid, target})
	return nil
}

// Snapshots (return copies) for assertions:

func (f *FakeChannel) Sent() []SentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]SentMsg, len(f.SentMessages))
	copy(cp, f.SentMessages)
	return cp
}

// PlatformReq is a recorded inbound request to a FakePlatform.
type PlatformReq struct {
	Method string
	Path   string
	Body   []byte
	Header http.Header
}

// PlatformHandler produces the response body (and optional status) for a path.
type PlatformHandler func(req PlatformReq) (status int, body any)

// FakePlatform is a configurable httptest.Server that records requests and
// returns canned JSON per "METHOD /path" route.
type FakePlatform struct {
	srv      *httptest.Server
	mu       sync.Mutex
	handlers map[string]PlatformHandler
	reqs     []PlatformReq
}

// NewFakePlatform starts a server. Close with Close().
func NewFakePlatform() *FakePlatform {
	p := &FakePlatform{handlers: map[string]PlatformHandler{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

// On registers a handler for "METHOD /path".
func (p *FakePlatform) On(route string, h PlatformHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[route] = h
}

// URL returns the server's base URL.
func (p *FakePlatform) URL() string { return p.srv.URL }

// Close shuts down the server.
func (p *FakePlatform) Close() { p.srv.Close() }

// Requests returns a snapshot of all recorded requests.
func (p *FakePlatform) Requests() []PlatformReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]PlatformReq, len(p.reqs))
	copy(cp, p.reqs)
	return cp
}

func (p *FakePlatform) handle(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 512)
	if r.Body != nil {
		buf := make([]byte, 1<<20)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body = append(body, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
	}
	req := PlatformReq{Method: r.Method, Path: r.URL.Path, Body: body, Header: r.Header.Clone()}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	h := p.handlers[r.Method+" "+r.URL.Path]
	p.mu.Unlock()

	if h == nil {
		http.NotFound(w, r)
		return
	}
	status, resp := h(req)
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if resp != nil {
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// AssertMessage fails if no row in messages has chat_jid=jid and content
// containing substr.
func AssertMessage(t *testing.T, db *sql.DB, jid, substr string) {
	t.Helper()
	rows, err := db.Query(`SELECT content FROM messages WHERE chat_jid = ?`, jid)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(c, substr) {
			return
		}
		found = append(found, c)
	}
	t.Fatalf("no message for jid=%q containing %q; saw %v", jid, substr, found)
}

// WaitForRow polls SELECT COUNT(*) from the given query until > 0 or timeout.
// query must be a full SELECT returning an int count.
func WaitForRow(t *testing.T, db *sql.DB, query string, args []any, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err == nil && n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("WaitForRow timeout after %s: %s args=%v", timeout, query, args)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
