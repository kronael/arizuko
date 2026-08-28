package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// readUGCID extracts the id LinkedIn assigns to a new post. That id is what a
// later edit, delete or like targets, so returning the wrong one — or an empty
// one with no error — silently detaches every follow-up action from the post.

func resp(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReadUGCIDPrefersTheRestLiHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-RestLi-Id", "urn:li:share:111")

	// The header is authoritative: LinkedIn returns the canonical urn there,
	// and the body's id (when present at all) is not always the same shape.
	got, err := readUGCID(resp(http.StatusCreated, h, `{"id":"urn:li:share:999"}`), "ugcPosts")
	if err != nil {
		t.Fatalf("readUGCID: %v", err)
	}
	if got != "urn:li:share:111" {
		t.Errorf("id = %q, want the X-RestLi-Id header", got)
	}
}

func TestReadUGCIDFallsBackToTheBody(t *testing.T) {
	got, err := readUGCID(resp(http.StatusOK, nil, `{"id":"urn:li:share:222"}`), "ugcPosts")
	if err != nil {
		t.Fatalf("readUGCID: %v", err)
	}
	if got != "urn:li:share:222" {
		t.Errorf("id = %q, want the body's id", got)
	}
}

// A 2xx with neither header nor body id is the shape that detaches follow-up
// actions. It is not an error today — assert the current contract so a change
// is a decision, and so the empty value is visible.
func TestReadUGCIDIsEmptyWhenTheResponseCarriesNoID(t *testing.T) {
	for _, body := range []string{`{}`, ``, `not json`} {
		got, err := readUGCID(resp(http.StatusOK, nil, body), "ugcPosts")
		if err != nil {
			t.Errorf("body %q: %v", body, err)
		}
		if got != "" {
			t.Errorf("body %q gave id %q, want empty", body, got)
		}
	}
}

// Every non-2xx is an error carrying the platform's own message. Without the
// body the operator sees "status 403" and nothing about which permission is
// missing — the most common LinkedIn failure.
func TestReadUGCIDSurfacesTheStatusAndTheBody(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		got, err := readUGCID(resp(status, nil, `{"message":"Not enough permissions"}`), "ugcPosts")
		if err == nil {
			t.Errorf("status %d was treated as success", status)
			continue
		}
		if got != "" {
			t.Errorf("status %d returned id %q alongside the error", status, got)
		}
		if !strings.Contains(err.Error(), "Not enough permissions") {
			t.Errorf("status %d: the error hides the platform's message: %v", status, err)
		}
		if !strings.Contains(err.Error(), "ugcPosts") {
			t.Errorf("status %d: the error does not say which call failed: %v", status, err)
		}
	}
}

// 3xx is not success. LinkedIn redirects an unauthenticated call to a login
// page, and treating that as a created post would report a post that does not
// exist.
func TestReadUGCIDRejectsARedirect(t *testing.T) {
	if _, err := readUGCID(resp(http.StatusFound, nil, "<html>login</html>"), "ugcPosts"); err == nil {
		t.Error("a 302 was treated as a successful post")
	}
}

// Client construction

// An unparseable or non-positive poll interval must land on the 5m default.
// Zero would spin the poll loop against LinkedIn's rate limiter.
func TestNewLinkClientRefusesNoTokenAndFloorsThePollInterval(t *testing.T) {
	for _, interval := range []string{"", "0", "0s", "-1m", "nonsense", "5m"} {
		_, err := newLinkClient(config{
			Name: "li", DataDir: t.TempDir(), PollInterval: interval,
		})
		if err == nil {
			t.Fatalf("interval %q: newLinkClient succeeded with no access token", interval)
		}
		if !strings.Contains(err.Error(), "LINKEDIN_ACCESS_TOKEN") {
			t.Errorf("interval %q: the error must name the missing credential, got %v", interval, err)
		}
	}
}

// isConnected reflects the auth flag the refresh path clears. /health reads it,
// so a value that never changes makes the adapter report healthy while it is
// locked out.
func TestIsConnectedTracksTheAuthFlag(t *testing.T) {
	lc := &linkClient{}
	if lc.isConnected() {
		t.Error("a fresh client reported connected before it authenticated")
	}
	lc.authed.Store(true)
	if !lc.isConnected() {
		t.Error("isConnected did not follow the auth flag")
	}
	lc.authed.Store(false)
	if lc.isConnected() {
		t.Error("isConnected stayed true after auth was cleared")
	}
}

// Typing is a no-op: LinkedIn has no typing indicator, and every outbound path
// calls it unconditionally, so an error or a panic here breaks sends that are
// fine.
func TestTypingIsASilentNoOp(t *testing.T) {
	lc := &linkClient{}
	lc.Typing("linkedin:urn:li:person:1", true)
	lc.Typing("linkedin:urn:li:person:1", false)
}
