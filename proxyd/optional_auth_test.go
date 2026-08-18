package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// `optional` is the route-token surface's gate (specs/6/22 step 1): it replaced
// the `switch rt.Path` arm that named /chat/ and /hook/ and resolved their
// tokens. proxyd no longer knows those paths; webd resolves the token.

// optionalServer routes /chat/ at an upstream that echoes what it received.
func optionalServer(t *testing.T, ks *auth.KeySet, svc *auth.TokenSource) (*server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Echo-Sub", r.Header.Get("X-User-Sub"))
		w.Header().Set("X-Echo-Groups", r.Header.Get("X-User-Groups"))
		w.Header().Set("X-Echo-Folder", r.Header.Get("X-Folder"))
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:       config{authSecret: "testsecret"},
		ks:        ks,
		svc:       svc,
		viteProxy: httputil.NewSingleHostReverseProxy(u),
		rr:        newRoutesResource(nil, []Route{{Path: "/chat/", Backend: up.URL, Auth: "optional"}}),
	}
	return s, up
}

// An anonymous caller is forwarded — no identity, but WITH proxyd's transit
// proof. Without that bearer webd cannot tell a front-door request from one
// aimed at its port, and its stream gate refused every anonymous visitor.
func TestOptionalAuth_AnonForwardedWithTransitProof(t *testing.T) {
	authd, ks := fakeServiceAuthd(t)
	svc, err := auth.ServiceToken(authd.URL, "proxyd", "boot-proxyd")
	if err != nil {
		t.Fatal(err)
	}

	s, _ := optionalServer(t, ks, svc)
	req := httptest.NewRequest("GET", "/chat/tok/deep", nil)
	req.RemoteAddr = "3.3.3.3:3000"
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Echo-Sub"); got != "" {
		t.Errorf("X-User-Sub = %q, want empty for an anonymous caller", got)
	}
	bearer := strings.TrimPrefix(w.Header().Get("X-Echo-Auth"), "Bearer ")
	if bearer == "" {
		t.Fatal("no Authorization reached the backend: anonymous requests carry no transit proof")
	}
	probe := httptest.NewRequest("GET", "/", nil)
	probe.Header.Set("Authorization", "Bearer "+bearer)
	if !auth.ProxydTransit(probe, ks) {
		t.Error("forwarded bearer is not a service:proxyd transit proof")
	}
}

// A caller with a valid user bearer is identified: X-User-* stamped, and the
// caller's own bearer replaced by the transit proof so a backend cannot
// re-derive identity from it.
func TestOptionalAuth_BearerIdentifies(t *testing.T) {
	k, ks := es256KeySet(t)
	userTok, err := k.Sign(auth.TokenClaims{Sub: "user:google:123", Typ: "user"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	s, _ := optionalServer(t, ks, nil)
	req := httptest.NewRequest("GET", "/chat/tok/deep", nil)
	req.RemoteAddr = "3.3.3.3:3000"
	req.Header.Set("Authorization", "Bearer "+userTok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Echo-Sub"); got != "user:google:123" {
		t.Errorf("X-User-Sub = %q, want user:google:123", got)
	}
	if got := w.Header().Get("X-Echo-Auth"); strings.Contains(got, userTok) {
		t.Error("the caller's own bearer reached the backend; it must be replaced by the transit proof")
	}
}

// proxyd never stamps a folder on this surface any more — webd resolves the
// token. A stamp would be a second resolution of the same row, and the one that
// got it wrong sent `hook:a/b/c` to folder `a/b`, another tenant (BUGS L4).
func TestOptionalAuth_NoFolderStamp(t *testing.T) {
	s, _ := optionalServer(t, nil, nil)
	req := httptest.NewRequest("GET", "/chat/tok/deep", nil)
	req.RemoteAddr = "3.3.3.3:3000"
	req.Header.Set("X-Folder", "attacker-supplied")
	w := httptest.NewRecorder()
	s.route(w, req)

	if got := w.Header().Get("X-Echo-Folder"); got != "" {
		t.Errorf("X-Folder = %q, want empty — proxyd neither stamps nor forwards it", got)
	}
}

// The chat widget's live reply is Server-Sent Events. proxyd must FORWARD the
// frames as they are written, not buffer them until the upstream closes: a
// buffered stream looks alive in a unit test hitting webd directly and dead in
// a browser. Assert the first frame arrives while the upstream is still open.
func TestOptionalAuth_SSEStreamsThrough(t *testing.T) {
	released := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream recorder is not a Flusher")
			return
		}
		fmt.Fprint(w, "event: message\ndata: {\"id\":\"first\"}\n\n")
		f.Flush()
		<-released // hold the response open
		fmt.Fprint(w, "event: message\ndata: {\"id\":\"second\"}\n\n")
		f.Flush()
	}))
	defer up.Close()
	defer close(released)

	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:       config{authSecret: "testsecret"},
		viteProxy: httputil.NewSingleHostReverseProxy(u),
		rr:        newRoutesResource(nil, []Route{{Path: "/chat/", Backend: up.URL, Auth: "optional"}}),
	}
	front := httptest.NewServer(http.HandlerFunc(s.route))
	defer front.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", front.URL+"/chat/stream?token=t&topic=x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	line := make(chan string, 1)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				close(line)
				return
			}
			if strings.Contains(l, `"first"`) {
				line <- l
				return
			}
		}
	}()
	select {
	case l, ok := <-line:
		if !ok || !strings.Contains(l, `"first"`) {
			t.Fatal("stream closed before the first frame arrived")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first SSE frame did not arrive while the upstream was open — proxyd buffered the stream")
	}
}
