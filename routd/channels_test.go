package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

const testChanSecret = "test-channel-secret"

// newChannelRoutd builds a routd Server with a wired channel registry. The
// onRegister/onDeregister hooks record the live channels so the test can
// assert the same reuse path gated uses.
func newChannelRoutd(t *testing.T) (*Server, *chanreg.Registry) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, nil, nil, 0, "")
	reg := chanreg.New(testChanSecret)
	srv.SetChannelRegistry(reg, nil, nil)
	return srv, reg
}

// TestIngressJIDOwnership ports gated api.handleMessage's entry.Owns reject: a
// platform-scheme inbound must land under a registered channel's prefixes.
// Unowned platform JIDs are rejected (400 jid_prefix_mismatch); owned JIDs and
// internal schemes (web:/hook:, used by web chat + timed) are accepted.
func TestIngressJIDOwnership(t *testing.T) {
	srv, _ := newChannelRoutd(t)
	h := srv.Handler()
	// register a slack adapter owning slack:T1/.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", testChanSecret, map[string]any{
		"name": "slakd", "url": "http://slakd:8080",
		"jid_prefixes": []string{"slack:T1/"},
		"capabilities": map[string]bool{"send_text": true},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		name string
		jid  string
		want int
	}{
		{"owned slack", "slack:T1/C/U", http.StatusOK},
		{"unowned telegram", "telegram:42", http.StatusBadRequest},
		{"unowned slack team", "slack:T9/C/U", http.StatusBadRequest},
		{"web exempt", "web:demo", http.StatusOK},
		{"hook exempt", "hook:demo/gh", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := doJSON(t, h, "POST", "/v1/messages", "",
				apiv1.Message{ChatJID: c.jid, Sender: "u", Content: "hi", Verb: "message"})
			if r.Code != c.want {
				t.Fatalf("jid %q: code=%d want %d (%s)", c.jid, r.Code, c.want, r.Body.String())
			}
		})
	}
}

func chanReq(method, path, token string, body any) *http.Request {
	var rdr *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestChannelRegisterListDeregister is the channel-surface round trip:
// register an adapter (CHANNEL_SECRET-gated) → GET /v1/channels lists it in
// the shape `arizuko status` decodes → deregister with the session token
// removes it.
func TestChannelRegisterListDeregister(t *testing.T) {
	srv, _ := newChannelRoutd(t)
	h := srv.Handler()

	reg := chanReq("POST", "/v1/channels/register", testChanSecret, map[string]any{
		"name": "slakd", "url": "http://slakd:8080",
		"jid_prefixes": []string{"slack:T1/"},
		"capabilities": map[string]bool{"send_text": true},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reg)
	if rec.Code != 200 {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rr struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rr)
	if !rr.OK || rr.Token == "" {
		t.Fatalf("register resp=%+v", rr)
	}

	// GET /v1/channels — must decode into the shape arizuko status reads.
	lrec := httptest.NewRecorder()
	h.ServeHTTP(lrec, chanReq("GET", "/v1/channels", testChanSecret, nil))
	if lrec.Code != 200 {
		t.Fatalf("list status=%d", lrec.Code)
	}
	var list struct {
		OK       bool `json:"ok"`
		Channels []struct {
			Name         string          `json:"name"`
			URL          string          `json:"url"`
			JIDPrefixes  []string        `json:"jid_prefixes"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(lrec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Channels) != 1 || list.Channels[0].Name != "slakd" ||
		list.Channels[0].URL != "http://slakd:8080" {
		t.Fatalf("list=%+v", list.Channels)
	}
	if len(list.Channels[0].JIDPrefixes) != 1 || list.Channels[0].JIDPrefixes[0] != "slack:T1/" {
		t.Fatalf("prefixes=%+v", list.Channels[0].JIDPrefixes)
	}

	// deregister with the session token.
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, chanReq("POST", "/v1/channels/deregister", rr.Token, nil))
	if drec.Code != 200 {
		t.Fatalf("deregister status=%d body=%s", drec.Code, drec.Body.String())
	}

	// list is empty again.
	lrec2 := httptest.NewRecorder()
	h.ServeHTTP(lrec2, chanReq("GET", "/v1/channels", testChanSecret, nil))
	json.Unmarshal(lrec2.Body.Bytes(), &list)
	if len(list.Channels) != 0 {
		t.Fatalf("post-deregister channels=%+v", list.Channels)
	}
}

// TestChannelRegisterRejectsBadSecret confirms the CHANNEL_SECRET gate: a
// wrong bearer is 401 and registers nothing.
func TestChannelRegisterRejectsBadSecret(t *testing.T) {
	srv, reg := newChannelRoutd(t)
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", "wrong-secret", map[string]any{
		"name": "x", "url": "http://x:8080", "jid_prefixes": []string{"x:"},
	}))
	if rec.Code != 401 {
		t.Fatalf("bad-secret status=%d want 401", rec.Code)
	}
	if reg.Get("x") != nil {
		t.Fatal("rejected register still created an entry")
	}
}

// TestChannelEndpointsUnmountedWithoutRegistry confirms a Server with no
// registry (pure REST tests) doesn't expose the channel surface — the route
// is absent, so the mux 405s the method or 404s the path.
func TestChannelEndpointsUnmountedWithoutRegistry(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, nil, nil, 0, "")
	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("GET", "/v1/channels", testChanSecret, nil))
	if rec.Code == 200 {
		t.Fatalf("GET /v1/channels served without a registry (status=%d)", rec.Code)
	}
}
