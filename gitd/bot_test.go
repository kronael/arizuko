package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

func fakeGitHub(t *testing.T, status int, reply string) (*httptest.Server, *[]*http.Request, *[]string) {
	t.Helper()
	var reqs []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqs = append(reqs, r)
		bodies = append(bodies, string(raw))
		if r.URL.Path == "/user" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"login":"arizuko-bot"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &bodies
}

func TestSend_PostsComment(t *testing.T) {
	gh, reqs, bodies := fakeGitHub(t, 201, `{"id":777,"html_url":"https://github.com/kronael/arizuko/pull/12#issuecomment-777"}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	id, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "looks good"})
	if err != nil || id != "gh-c777" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	r := (*reqs)[0]
	if r.Method != "POST" || r.URL.Path != "/repos/kronael/arizuko/issues/12/comments" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer ghp_test" {
		t.Errorf("auth header = %q", r.Header.Get("Authorization"))
	}
	var body map[string]string
	_ = json.Unmarshal([]byte((*bodies)[0]), &body)
	if body["body"] != "looks good" {
		t.Errorf("body = %q", (*bodies)[0])
	}
}

func TestSend_Endpoint(t *testing.T) {
	cases := []struct{ name, jid, threadID, path string }{
		{"review thread", "github:kronael/arizuko/pr/12", "pr/12/review/5678901200",
			"/repos/kronael/arizuko/pulls/12/comments/5678901200/replies"},
		{"pr topic", "github:kronael/arizuko/pr/12", "pr/12", "/repos/kronael/arizuko/issues/12/comments"},
		{"no thread", "github:kronael/arizuko/pr/12", "", "/repos/kronael/arizuko/issues/12/comments"},
		{"issue topic", "github:kronael/arizuko/issue/3", "issue/3", "/repos/kronael/arizuko/issues/3/comments"},
		{"review topic of another pr", "github:kronael/arizuko/pr/12", "pr/13/review/5678901200",
			"/repos/kronael/arizuko/issues/12/comments"},
		{"sticky topic", "github:kronael/arizuko/pr/12", "#triage", "/repos/kronael/arizuko/issues/12/comments"},
	}
	for _, c := range cases {
		gh, reqs, _ := fakeGitHub(t, 201, `{"id":888}`)
		b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
		id, err := b.Send(chanlib.SendRequest{ChatJID: c.jid, ThreadID: c.threadID, Content: "x"})
		if err != nil || id != "gh-c888" {
			t.Fatalf("%s: Send = %q, %v", c.name, id, err)
		}
		if r := (*reqs)[0]; r.Method != "POST" || r.URL.Path != c.path {
			t.Errorf("%s: request = %s %s, want POST %s", c.name, r.Method, r.URL.Path, c.path)
		}
	}
}

func TestSend_ReviewRootRefused(t *testing.T) {
	gh, _, _ := fakeGitHub(t, 404, `{"message":"Not Found"}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	_, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", ThreadID: "pr/12/review/1", Content: "x"})
	if !errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("404 on a review root err = %v, want ErrPermanent", err)
	}
}

func TestSend_Refusals(t *testing.T) {
	gh, _, _ := fakeGitHub(t, 422, `{"message":"Validation Failed"}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	if _, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko", Content: "x"}); !errors.Is(err, chanlib.ErrInvalidRequest) {
		t.Errorf("repo-level jid err = %v, want ErrInvalidRequest", err)
	}
	if _, err := b.Send(chanlib.SendRequest{ChatJID: "slack:x/channel/y", Content: "x"}); !errors.Is(err, chanlib.ErrInvalidRequest) {
		t.Errorf("foreign jid err = %v, want ErrInvalidRequest", err)
	}
	if _, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "x"}); !errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("422 err = %v, want ErrPermanent", err)
	}
	noToken := newBot(config{APIBase: gh.URL})
	if _, err := noToken.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "x"}); !errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("no-token err = %v, want ErrPermanent", err)
	}
	down, _, _ := fakeGitHub(t, 503, `{"message":"unavailable"}`)
	b = newBot(config{Token: "ghp_test", APIBase: down.URL})
	if _, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "x"}); !errors.Is(err, chanlib.ErrNotDelivered) {
		t.Errorf("503 err = %v, want ErrNotDelivered", err)
	}
}

func TestStart_LearnsLogin(t *testing.T) {
	gh, _, _ := fakeGitHub(t, 201, `{}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	if err := b.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Login() != "arizuko-bot" || !b.isConnected() {
		t.Errorf("login=%q connected=%v", b.Login(), b.isConnected())
	}
	ingress := newBot(config{APIBase: gh.URL})
	if err := ingress.start(context.Background()); err != nil || ingress.Login() != "" || !ingress.isConnected() {
		t.Errorf("ingress-only start: err=%v login=%q connected=%v", err, ingress.Login(), ingress.isConnected())
	}
}
