package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// fakeGitHubAPI answers only the paths a test names, so a request to any other
// endpoint comes back 404 and the test says which path the code chose.
func fakeGitHubAPI(t *testing.T, bodies map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path)
		body, ok := bodies[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		if r.Method == "POST" {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// An answer to an inline review comment lands under its own code line: the
// review-reply endpoint, addressed by the comment that ROOTED the thread,
// which is the only target GitHub accepts.
func TestSend_InlineReviewReplyTargetsThreadRoot(t *testing.T) {
	const root = "POST /repos/kronael/arizuko/pulls/12/comments/5678901200/replies"
	for _, c := range []struct{ name, replyTo, lookup string }{
		{"the turn was anchored on the thread root", "gh-rc5678901200", `{"id":5678901200}`},
		{"the turn was anchored on a reply in the thread", "gh-rc5678901234",
			`{"id":5678901234,"in_reply_to_id":5678901200}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh, seen := fakeGitHubAPI(t, map[string]string{
				"GET /repos/kronael/arizuko/pulls/comments/" + strings.TrimPrefix(c.replyTo, "gh-rc"): c.lookup,
				root: `{"id":888}`,
			})
			b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
			id, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12",
				ReplyTo: c.replyTo, Content: "fixed, thanks"})
			if err != nil || id != "gh-rc888" {
				t.Fatalf("Send = %q, %v; requests %v", id, err, *seen)
			}
			if got := (*seen)[len(*seen)-1]; got != root {
				t.Errorf("the reply went to %q, want %q", got, root)
			}
		})
	}
}

// Every other reply stays on the issue-comments endpoint GitHub shares between
// PR and issue conversations, and costs no thread lookup. A review id and an
// issue-comment id name objects in separate spaces, and an issue carries no
// inline thread at all.
func TestSend_OrdinaryCommentTargetsIssueComments(t *testing.T) {
	for _, c := range []struct{ name, jid, replyTo, path string }{
		{"no reply target", "github:kronael/arizuko/pr/12", "", "/repos/kronael/arizuko/issues/12/comments"},
		{"an issue comment on a pr", "github:kronael/arizuko/pr/12", "gh-c4567890123", "/repos/kronael/arizuko/issues/12/comments"},
		{"a review summary", "github:kronael/arizuko/pr/12", "gh-r5678901", "/repos/kronael/arizuko/issues/12/comments"},
		{"the bot's own outbound row", "github:kronael/arizuko/pr/12", "out-9f3a21bc", "/repos/kronael/arizuko/issues/12/comments"},
		{"an issue", "github:kronael/arizuko/issue/3", "gh-c4567890123", "/repos/kronael/arizuko/issues/3/comments"},
		{"a review id on an issue jid", "github:kronael/arizuko/issue/3", "gh-rc5678901200", "/repos/kronael/arizuko/issues/3/comments"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh, seen := fakeGitHubAPI(t, map[string]string{"POST " + c.path: `{"id":888}`})
			b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
			id, err := b.Send(chanlib.SendRequest{ChatJID: c.jid, ReplyTo: c.replyTo, Content: "x"})
			if err != nil || id != "gh-c888" {
				t.Fatalf("Send = %q, %v; requests %v", id, err, *seen)
			}
			if len(*seen) != 1 || (*seen)[0] != "POST "+c.path {
				t.Errorf("requests = %v, want the one POST %s", *seen, c.path)
			}
		})
	}
}

// A thread GitHub will not show us is reported, never redirected to the
// conversation tab where the reviewer reading the diff would not see it.
func TestSend_UnknownReviewThreadRefused(t *testing.T) {
	gh, seen := fakeGitHubAPI(t, map[string]string{
		"POST /repos/kronael/arizuko/issues/12/comments": `{"id":888}`,
	})
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	_, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12",
		ReplyTo: "gh-rc5678901200", Content: "x"})
	if !errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("err = %v, want ErrPermanent", err)
	}
	if len(*seen) != 1 {
		t.Errorf("requests = %v, want the lookup alone and no fallback comment", *seen)
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

// A transport failure leaves the outcome unknown: the client timeout fires
// after the request body went out, so GitHub may already hold the comment.
// ErrNotDelivered releases the dedup claim and the sweep posts a SECOND
// comment (BUGS L413). Unclassified, the claim stays and routd dead-letters.
func TestSend_TransportFailureIsUnclassified(t *testing.T) {
	block := make(chan struct{})
	gh := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	// LIFO: the handler is released BEFORE Close waits for it to return.
	defer gh.Close()
	defer close(block)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	b.http = &http.Client{Timeout: 50 * time.Millisecond}
	_, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "x"})
	if err == nil {
		t.Fatal("Send reported success on a timeout")
	}
	if errors.Is(err, chanlib.ErrNotDelivered) {
		t.Errorf("timeout err = %v, want unclassified: a lost response may have posted the comment", err)
	}
	if errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("timeout err = %v, want unclassified: a retry can still deliver it", err)
	}
}

// A GET creates nothing, so a lost response cannot have posted the reply.
// Leaving the thread lookup unclassified holds the dedup claim for a comment
// GitHub never saw, and routd dead-letters it an hour later having posted
// nothing (BUGS L426).
func TestSend_ThreadLookupTimeoutReleasesTheClaim(t *testing.T) {
	block := make(chan struct{})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			<-block
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":888}`))
	}))
	// LIFO: the handler is released BEFORE Close waits for it to return.
	defer gh.Close()
	defer close(block)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	b.http = &http.Client{Timeout: 50 * time.Millisecond}

	_, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12",
		ReplyTo: "gh-rc5678901200", Content: "x"})

	if !errors.Is(err, chanlib.ErrNotDelivered) {
		t.Errorf("thread lookup timeout err = %v, want ErrNotDelivered: nothing was posted", err)
	}
}

// GitHub answers a rate limit with 403 as well as 429, and a permanent refusal
// dead-letters the reply at once, so the two 403s must not read the same.
func TestCall_RateLimited403IsRetryable(t *testing.T) {
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    error
	}{
		{"a refused credential", nil, chanlib.ErrPermanent},
		{"a primary rate limit", map[string]string{"X-RateLimit-Remaining": "0"}, chanlib.ErrNotDelivered},
		{"a secondary rate limit", map[string]string{"Retry-After": "60"}, chanlib.ErrNotDelivered},
	} {
		t.Run(c.name, func(t *testing.T) {
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
			}))
			defer gh.Close()
			b := newBot(config{Token: "ghp_test", APIBase: gh.URL})

			_, err := b.Send(chanlib.SendRequest{ChatJID: "github:kronael/arizuko/pr/12", Content: "x"})

			if !errors.Is(err, c.want) {
				t.Errorf("403 err = %v, want %v", err, c.want)
			}
			if c.want == chanlib.ErrNotDelivered && errors.Is(err, chanlib.ErrPermanent) {
				t.Errorf("403 err = %v is permanent too; the reply dead-letters", err)
			}
		})
	}
}

// editFake is a GitHub that holds comments by path, each with an author, and
// records every request. A path it does not hold answers 404.
type editFake struct {
	srv     *httptest.Server
	authors map[string]string
	seen    []string
	bodies  []string
}

func newEditFake(t *testing.T, authors map[string]string) *editFake {
	t.Helper()
	f := &editFake{authors: authors}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(raw))
		author, ok := f.authors[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"user":{"login":"` + author + `"}}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func editBot(f *editFake) *bot {
	b := newBot(config{Token: "ghp_test", APIBase: f.srv.URL})
	b.login.Store("arizuko-bot")
	return b
}

const editJID = "github:kronael/arizuko/pr/12"

// The id prefix picks the endpoint, as it does for a reaction: a conversation
// comment and an inline review comment are separate id spaces.
func TestEdit_PicksEndpointByIDPrefix(t *testing.T) {
	for _, c := range []struct{ id, path string }{
		{"gh-c777", "/repos/kronael/arizuko/issues/comments/777"},
		{"gh-rc888", "/repos/kronael/arizuko/pulls/comments/888"},
	} {
		t.Run(c.id, func(t *testing.T) {
			f := newEditFake(t, map[string]string{c.path: "arizuko-bot"})
			err := editBot(f).Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: c.id, Content: "✓ done"})
			if err != nil {
				t.Fatalf("Edit = %v; requests %v", err, f.seen)
			}
			want := []string{"GET " + c.path, "PATCH " + c.path}
			if strings.Join(f.seen, ",") != strings.Join(want, ",") {
				t.Errorf("requests = %v, want %v", f.seen, want)
			}
			if got := f.bodies[1]; got != `{"body":"✓ done"}` {
				t.Errorf("PATCH body = %s, want only {\"body\":\"✓ done\"}", got)
			}
		})
	}
}

// An id gitd did not mint as a comment is refused before any call.
func TestEdit_ForeignIDIsInvalidRequest(t *testing.T) {
	for _, id := range []string{"", "777", "gh-r777", "gh-c", "gh-c0", "gh-cx", "gh-rc-1"} {
		t.Run("id="+id, func(t *testing.T) {
			f := newEditFake(t, nil)
			err := editBot(f).Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: id, Content: "x"})
			if !errors.Is(err, chanlib.ErrInvalidRequest) {
				t.Errorf("Edit(%q) = %v, want ErrInvalidRequest", id, err)
			}
			if len(f.seen) != 0 {
				t.Errorf("Edit(%q) called GitHub: %v", id, f.seen)
			}
		})
	}
}

// gitd edits only a comment it wrote. A token with write access may edit
// anyone's comment on GitHub's side, so the refusal is gitd's: the comment is
// read, its author differs, and no PATCH is sent.
func TestEdit_RefusesACommentGitdDidNotWrite(t *testing.T) {
	const path = "/repos/kronael/arizuko/issues/comments/777"
	for _, c := range []struct{ name, author string }{
		{"another user", "octocat"},
		{"a login that only prefixes ours", "arizuko-bot-2"},
		{"a deleted account", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newEditFake(t, map[string]string{path: c.author})
			err := editBot(f).Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: "gh-c777", Content: "x"})
			if !errors.Is(err, chanlib.ErrPermanent) {
				t.Fatalf("Edit = %v, want ErrPermanent", err)
			}
			for _, r := range f.seen {
				if strings.HasPrefix(r, "PATCH") {
					t.Errorf("gitd sent %q on a comment it did not write", r)
				}
			}
		})
	}
	t.Run("no token", func(t *testing.T) {
		f := newEditFake(t, map[string]string{path: "arizuko-bot"})
		b := editBot(f)
		b.cfg.Token = ""
		err := b.Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: "gh-c777", Content: "x"})
		if !errors.Is(err, chanlib.ErrUnsupported) || len(f.seen) != 0 {
			t.Errorf("Edit = %v, requests %v; want ErrUnsupported and no call", err, f.seen)
		}
	})
	t.Run("login not learned", func(t *testing.T) {
		f := newEditFake(t, map[string]string{path: "arizuko-bot"})
		b := editBot(f)
		b.login.Store("")
		err := b.Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: "gh-c777", Content: "x"})
		if !errors.Is(err, chanlib.ErrPermanent) || len(f.seen) != 0 {
			t.Errorf("Edit = %v, requests %v; want ErrPermanent and no call", err, f.seen)
		}
	})
}

// GitHub's own refusal of the edit is classified as a response GitHub sent.
func TestEdit_GitHubRefusalIsClassified(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"login":"arizuko-bot"}}`))
	}))
	defer gh.Close()
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	b.login.Store("arizuko-bot")
	err := b.Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: "gh-c777", Content: "x"})
	if !errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("Edit = %v, want ErrPermanent for a 422", err)
	}
}

// A lost response leaves the outcome unknown, so a transport failure carries no
// class, as for Send.
func TestEdit_TransportFailureIsUnclassified(t *testing.T) {
	block := make(chan struct{})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"user":{"login":"arizuko-bot"}}`))
			return
		}
		<-block
	}))
	defer gh.Close()
	defer close(block)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	b.login.Store("arizuko-bot")
	b.http = &http.Client{Timeout: 50 * time.Millisecond}
	err := b.Edit(chanlib.EditRequest{ChatJID: editJID, TargetID: "gh-c777", Content: "x"})
	if err == nil {
		t.Fatal("Edit reported success on a timeout")
	}
	if errors.Is(err, chanlib.ErrNotDelivered) || errors.Is(err, chanlib.ErrPermanent) {
		t.Errorf("timeout err = %v, want unclassified", err)
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSendFile_PostsTextAsFencedBlock(t *testing.T) {
	gh, reqs, bodies := fakeGitHub(t, 201, `{"id":901}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	path := writeTemp(t, "main.go", []byte("package main\n\n// ``` inside\nfunc main() {}\n"))
	id, err := b.SendFile("github:kronael/arizuko/pr/12", path, "main.go", "the entrypoint", "", "")
	if err != nil || id != "gh-c901" {
		t.Fatalf("SendFile = %q, %v", id, err)
	}
	if r := (*reqs)[0]; r.Method != "POST" || r.URL.Path != "/repos/kronael/arizuko/issues/12/comments" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte((*bodies)[0]), &body)
	want := "the entrypoint\n\n`main.go`\n\n````go\npackage main\n\n// ``` inside\nfunc main() {}\n````"
	if body["body"] != want {
		t.Errorf("body = %q\nwant   %q", body["body"], want)
	}
}

func TestSendFile_RefusesBeforeAnyRequest(t *testing.T) {
	gh, reqs, _ := fakeGitHub(t, 201, `{"id":1}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	jid := "github:kronael/arizuko/pr/12"

	t.Run("oversize", func(t *testing.T) {
		path := writeTemp(t, "big.txt", []byte(strings.Repeat("x", 65536)))
		_, err := b.SendFile(jid, path, "big.txt", "", "", "")
		if !errors.Is(err, chanlib.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
		if !strings.Contains(err.Error(), "github connector") {
			t.Errorf("refusal names no connector tool: %v", err)
		}
	})
	t.Run("fits exactly at the cap", func(t *testing.T) {
		overhead := len(fileComment("a.txt", "", ""))
		path := writeTemp(t, "a.txt", []byte(strings.Repeat("x", 65536-overhead-1)+"\n"))
		if _, err := b.SendFile(jid, path, "a.txt", "", "", ""); err != nil {
			t.Fatalf("a file that fits was refused: %v", err)
		}
	})
	*reqs = (*reqs)[:0]

	t.Run("binary", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"x.png": {0x89, 'P', 'N', 'G', 0, 1, 2},
			"x.bin": {0xff, 0xfe, 'a'},
		} {
			path := writeTemp(t, name, data)
			_, err := b.SendFile(jid, path, name, "", "", "")
			if !errors.Is(err, chanlib.ErrUnsupported) {
				t.Fatalf("%s: err = %v, want Unsupported", name, err)
			}
			if !strings.Contains(err.Error(), "github connector") {
				t.Errorf("%s: refusal names no connector tool: %v", name, err)
			}
		}
	})
	if len(*reqs) != 0 {
		t.Errorf("refusals made %d request(s) to GitHub", len(*reqs))
	}
}

func TestSendFile_WithoutTokenIsUnsupported(t *testing.T) {
	b := newBot(config{})
	_, err := b.SendFile("github:kronael/arizuko/pr/12", "x.txt", "x.txt", "", "", "")
	if !errors.Is(err, chanlib.ErrUnsupported) {
		t.Fatalf("err = %v, want Unsupported", err)
	}
}
