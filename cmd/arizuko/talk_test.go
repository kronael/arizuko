package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// --- argument grammar ---------------------------------------------------
//
// Two sigils, no collision: @ picks the chat, --thread picks the thread. Getting
// that wrong sends the turn to the right agent in the wrong conversation,
// which reads as amnesia rather than as a bug.

func TestParseTalkArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want talkFlags
	}{
		{"no arguments is the one chat on the default thread",
			nil, talkFlags{Thread: "talk"}},
		{"--prompt with an inline prompt is one turn",
			[]string{"--prompt", "what broke?"},
			talkFlags{Thread: "talk", OneShot: true, Prompt: "what broke?"}},
		{"--prompt with nothing after it reads stdin",
			[]string{"--prompt"},
			talkFlags{Thread: "talk", OneShot: true}},
		{"--prompt before another flag still reads stdin",
			[]string{"--prompt", "--thread", "bugfix"},
			talkFlags{Thread: "bugfix", OneShot: true}},
		{"@name picks the chat",
			[]string{"@work", "--prompt", "hi"},
			talkFlags{Section: "work", Thread: "talk", OneShot: true, Prompt: "hi"}},
		{"--thread picks the thread",
			[]string{"--thread", "bugfix"},
			talkFlags{Thread: "bugfix"}},
		{"-t is the token, as in every command",
			[]string{"-t", "abc"},
			talkFlags{Thread: "talk", Token: "abc"}},
		{"-g names the folder",
			[]string{"-g", "corp/eng"},
			talkFlags{Thread: "talk", Group: "corp/eng"}},
		{"--url and --token override the config",
			[]string{"--url", "https://h", "--token", "abc"},
			talkFlags{Thread: "talk", URL: "https://h", Token: "abc"}},
		{"a prompt after --prompt may look like a chat name",
			[]string{"--prompt", "@ everyone"},
			talkFlags{Thread: "talk", OneShot: true, Prompt: "@ everyone"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTalkArgs(c.args)
			if err != nil {
				t.Fatalf("parse %v: %v", c.args, err)
			}
			if got != c.want {
				t.Errorf("parse %v =\n  %+v\nwant\n  %+v", c.args, got, c.want)
			}
		})
	}
}

// The thread default is a FIXED literal. No last-used cache, no state file.
func TestParseTalkArgsDefaultsTheThreadToTheLiteralTalk(t *testing.T) {
	got, err := parseTalkArgs(nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Thread != "talk" {
		t.Errorf("default thread = %q, want the literal \"talk\"", got.Thread)
	}
	withT, _ := parseTalkArgs([]string{"--thread", "bugfix"})
	if withT.Thread != "bugfix" {
		t.Errorf("--thread bugfix gave thread %q", withT.Thread)
	}
}

func TestParseTalkArgsRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"--thread"},          // flag with no value
		{"-g"},                //
		{"--token"},           //
		{"@"},                 // bare sigil
		{"@a", "@b"},          // two chats named
		{"solo/inbox"},        // a bare positional: -g was meant
		{"--thread", "", "x"}, // an empty thread name
		{"-p", "hi"},          // prompt is long-only
	} {
		if _, err := parseTalkArgs(args); err == nil {
			t.Errorf("parseTalkArgs(%v) was accepted; want an error", args)
		}
	}
}

// --- the prompt sources -------------------------------------------------

func TestTalkPromptsInlineAndStdin(t *testing.T) {
	inline, err := talkPrompts(talkFlags{OneShot: true, Prompt: "what broke?"}, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	assertPrompts(t, "-p inline", inline, []string{"what broke?"})

	stdin, err := talkPrompts(talkFlags{OneShot: true}, strings.NewReader("line one\nline two\n"), io.Discard)
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	// -p reads the WHOLE of stdin as ONE prompt: a heredoc is one question.
	assertPrompts(t, "-p stdin", stdin, []string{"line one\nline two\n"})

	repl, err := talkPrompts(talkFlags{}, strings.NewReader("first\nsecond\n"), io.Discard)
	if err != nil {
		t.Fatalf("repl: %v", err)
	}
	assertPrompts(t, "repl", repl, []string{"first", "second"})
}

// An empty -p is a no-op turn dressed as success. Fail loud instead.
func TestTalkPromptsRefusesAnEmptyOneShot(t *testing.T) {
	if _, err := talkPrompts(talkFlags{OneShot: true}, strings.NewReader("  \n"), io.Discard); err == nil {
		t.Error("-p with blank stdin was accepted")
	}
}

func assertPrompts(t *testing.T, what string, next promptSource, want []string) {
	t.Helper()
	var got []string
	for {
		p, ok := next()
		if !ok {
			break
		}
		got = append(got, p)
	}
	if len(got) != len(want) {
		t.Fatalf("%s yielded %d prompts %q, want %d %q", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s prompt %d = %q, want %q", what, i, got[i], want[i])
		}
	}
}

// --- the wire -----------------------------------------------------------

// fakeInstance is a real streamable-HTTP MCP server on /mcp plus the /me SSE
// stream, so these tests exercise the same client shape a live proxyd sees. A
// stub JSON endpoint would pass while the MCP handshake did not.
type fakeInstance struct {
	*httptest.Server
	sent     chan map[string]string // folder/content/topic of each send
	folders  []string
	sseHits  chan string // the SSE path each turn asked for
	order    []string    // "sse" / "send", in the order the server saw them
	reply    string
	status   string
	mcpAuth  string
	sseAuth  string
	authFail bool
}

func newFakeInstance(t *testing.T, folders []string) *fakeInstance {
	t.Helper()
	fi := &fakeInstance{
		sent: make(chan map[string]string, 8), folders: folders,
		sseHits: make(chan string, 8), reply: "the disk filled up", status: "success",
	}

	srv := mcpserver.NewMCPServer("arizuko-web", "1.0")
	srv.AddTool(mcp.NewTool("list_groups"),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			rows := make([]map[string]string, 0, len(fi.folders))
			for _, f := range fi.folders {
				rows = append(rows, map[string]string{"folder": f})
			}
			out, _ := json.Marshal(map[string]any{"groups": rows})
			return mcp.NewToolResultText(string(out)), nil
		})
	srv.AddTool(mcp.NewTool("send",
		mcp.WithString("folder", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("topic"),
	), func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fi.order = append(fi.order, "send")
		fi.sent <- map[string]string{
			"folder":  r.GetString("folder", ""),
			"content": r.GetString("content", ""),
			"topic":   r.GetString("topic", ""),
		}
		out, _ := json.Marshal(map[string]any{"id": "msg1", "topic": r.GetString("topic", "")})
		return mcp.NewToolResultText(string(out)), nil
	})
	mcpHandler := mcpserver.NewStreamableHTTPServer(srv, mcpserver.WithStateLess(true))

	mux := http.NewServeMux()
	// proxyd answers a rejected bearer with a 303 to /auth/login, never a 401.
	gate := func(h http.Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/me/") {
				fi.sseAuth = r.Header.Get("Authorization")
			} else {
				fi.mcpAuth = r.Header.Get("Authorization")
			}
			if fi.authFail {
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}
			h.ServeHTTP(w, r)
		}
	}
	mux.Handle("/mcp", gate(mcpHandler))
	mux.HandleFunc("GET /me/chats/{rest...}", gate(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fi.order = append(fi.order, "sse")
			fi.sseHits <- r.URL.Path
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl, _ := w.(http.Flusher)
			write := func(s string) {
				_, _ = fmt.Fprint(w, s)
				if fl != nil {
					fl.Flush()
				}
			}
			write(": ok\n\n")
			// The caller's own message comes back on the same topic as role=user.
			write("event: message\ndata: " + `{"role":"user","content":"ECHO OF MY OWN PROMPT"}` + "\n\n")
			write("event: message\ndata: " + fmt.Sprintf(`{"role":"assistant","content":%q}`, fi.reply) + "\n\n")
			write("event: round_done\ndata: " + fmt.Sprintf(`{"turn_id":"msg1","status":%q}`, fi.status) + "\n\n")
		})))

	fi.Server = httptest.NewServer(mux)
	t.Cleanup(fi.Close)
	return fi
}

func (fi *fakeInstance) session(t *testing.T, group, thread string) *talkSession {
	t.Helper()
	s, err := newTalkSession(context.Background(),
		talkEntry{URL: fi.URL, Token: "bearer-abc", Group: group}, thread, io.Discard)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(s.close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.resolveFolder(ctx); err != nil {
		t.Fatalf("resolveFolder: %v", err)
	}
	return s
}

// One turn end to end: the bearer travels, the send carries folder+thread, and
// the round ends on round_done.
func TestTalkTurnSendsAndReadsTheRound(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	s := fi.session(t, "", "talk")

	if code := s.turn(context.Background(), "what broke?"); code != 0 {
		t.Fatalf("turn = %d, want 0 for a successful round", code)
	}
	got := <-fi.sent
	if got["folder"] != "solo/inbox" || got["content"] != "what broke?" || got["topic"] != "talk" {
		t.Errorf("send args = %v, want folder=solo/inbox content=\"what broke?\" topic=talk", got)
	}
	// Both surfaces need the bearer. Asserting one shared field would let a
	// dropped SSE bearer hide behind the MCP request that ran after it.
	if fi.mcpAuth != "Bearer bearer-abc" {
		t.Errorf("/mcp Authorization = %q, want the configured bearer", fi.mcpAuth)
	}
	if fi.sseAuth != "Bearer bearer-abc" {
		t.Errorf("sse Authorization = %q, want the configured bearer", fi.sseAuth)
	}
	if path := <-fi.sseHits; path != "/me/chats/solo/inbox/talk/sse" {
		t.Errorf("sse path = %q, want /me/chats/solo/inbox/talk/sse", path)
	}
	// SUBSCRIBE BEFORE SEND. The /me stream replays nothing, so a reply that
	// lands between the send and the subscribe is lost and the round never ends.
	if len(fi.order) < 2 || fi.order[0] != "sse" {
		t.Errorf("server saw %v, want the sse subscribe FIRST", fi.order)
	}
}

// -t names another thread, and the thread reaches BOTH the send and the stream.
// A thread that only reached one of them would split the conversation in half.
func TestTalkTurnCarriesTheThreadToSendAndStream(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	s := fi.session(t, "", "bugfix")

	if code := s.turn(context.Background(), "still broken"); code != 0 {
		t.Fatalf("turn = %d", code)
	}
	if got := <-fi.sent; got["topic"] != "bugfix" {
		t.Errorf("send topic = %q, want bugfix", got["topic"])
	}
	if path := <-fi.sseHits; path != "/me/chats/solo/inbox/bugfix/sse" {
		t.Errorf("sse path = %q, want the bugfix thread", path)
	}
}

// A failed round is a non-zero exit, not a silent success.
func TestTalkTurnReportsAFailedRound(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	fi.status = "failed"
	s := fi.session(t, "", "talk")
	if code := s.turn(context.Background(), "hi"); code != 1 {
		t.Errorf("turn on a failed round = %d, want 1", code)
	}
}

// -p is ONE ITERATION of the REPL loop. The proof is that both go through
// talkLoop and the only difference is how many prompts the source yields.
func TestTalkLoopRunsOnceForOneShotAndOncePerLineForTheREPL(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	s := fi.session(t, "", "talk")

	if code := talkLoop(context.Background(), s, onePrompt("what broke?")); code != 0 {
		t.Fatalf("one-shot loop = %d", code)
	}
	if n := len(fi.sent); n != 1 {
		t.Fatalf("-p sent %d messages, want exactly 1", n)
	}
	<-fi.sent
	<-fi.sseHits

	repl, err := talkPrompts(talkFlags{}, strings.NewReader("one\n\ntwo\n/quit\nthree\n"), io.Discard)
	if err != nil {
		t.Fatalf("prompts: %v", err)
	}
	if code := talkLoop(context.Background(), s, repl); code != 0 {
		t.Fatalf("repl loop = %d", code)
	}
	// Blank lines are skipped and /quit ends it before "three".
	var sent []string
	for len(fi.sent) > 0 {
		sent = append(sent, (<-fi.sent)["content"])
	}
	if len(sent) != 2 || sent[0] != "one" || sent[1] != "two" {
		t.Errorf("repl sent %q, want [one two] — blanks skipped, /quit ends it", sent)
	}
}

// The destination is inferred ONLY when there is exactly one; a caller who can
// reach several must say which, and the error lists them.
func TestResolveFolder(t *testing.T) {
	one := newFakeInstance(t, []string{"solo/inbox"})
	s := one.session(t, "", "talk")
	if s.folder != "solo/inbox" {
		t.Errorf("single reachable folder = %q, want it chosen silently", s.folder)
	}

	many := newFakeInstance(t, []string{"corp/eng", "corp/ops"})
	sm, err := newTalkSession(context.Background(),
		talkEntry{URL: many.URL, Token: "t"}, "talk", io.Discard)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(sm.close)
	err = sm.resolveFolder(context.Background())
	if err == nil {
		t.Fatal("two reachable folders resolved silently; -g must be required")
	}
	for _, want := range []string{"corp/eng", "corp/ops", "--group"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	none := newFakeInstance(t, nil)
	sn, err := newTalkSession(context.Background(),
		talkEntry{URL: none.URL, Token: "t"}, "talk", io.Discard)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(sn.close)
	if err := sn.resolveFolder(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "grant") {
		t.Errorf("no reachable folder gave %v, want an error naming the grant", err)
	}
}

// A configured group is taken as-is: no list_groups call, no ambiguity.
func TestResolveFolderPrefersTheConfiguredGroup(t *testing.T) {
	fi := newFakeInstance(t, []string{"corp/eng", "corp/ops"})
	s := fi.session(t, "corp/ops", "talk")
	if s.folder != "corp/ops" {
		t.Errorf("folder = %q, want the configured corp/ops even with two reachable", s.folder)
	}
}

// proxyd bounces a bad bearer to sign-in with a 303. Following it turns an auth
// failure into an HTML parse error; reporting it as one is worse than useless.
func TestTalkReportsARejectedBearerAsAnAuthFailure(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	fi.authFail = true
	_, err := newTalkSession(context.Background(),
		talkEntry{URL: fi.URL, Token: "stale"}, "talk", io.Discard)
	if err == nil {
		t.Fatal("a 303 to /auth/login was accepted as a session")
	}
	if !strings.Contains(err.Error(), "bearer") {
		t.Errorf("error = %q, want it to name the bearer as the problem", err)
	}
}

// The folder is escaped segment by segment. Collapsing it into one path element
// makes the URL depend on how the mux treats an encoded slash.
func TestTalkSSEURLEscapesEachSegment(t *testing.T) {
	s := &talkSession{entry: talkEntry{URL: "https://h"}, folder: "corp/eng ops", thread: "bug fix"}
	if got, want := s.sseURL(), "https://h/me/chats/corp/eng%20ops/bug%20fix/sse"; got != want {
		t.Errorf("sseURL = %q, want %q", got, want)
	}
}

// webd's /mcp send echoes the caller's own message back on the same topic as
// role=user. Printing it makes talk quote the operator at themselves.
//
// The assertion has to be on what is PRINTED: handleSSEFrame returns -1 for a
// skipped frame and for a printed one alike, so a return-value check passes
// whether or not the skip exists.
func TestHandleSSEFrameSkipsTheCallersOwnEcho(t *testing.T) {
	out := captureStdout(t, func() {
		handleSSEFrame("message", `{"role":"user","content":"MY OWN PROMPT"}`)
		handleSSEFrame("message", `{"role":"assistant","content":"the reply"}`)
		// Turn-stream frames (webd/turn.go) carry no role and must still print.
		handleSSEFrame("message", `{"content":"replayed frame","kind":"message"}`)
	})
	if strings.Contains(out, "MY OWN PROMPT") {
		t.Errorf("the caller's own message was printed back at them:\n%s", out)
	}
	for _, want := range []string{"the reply", "replayed frame"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// A turn's own reply must reach stdout, not just exit 0.
func TestTalkTurnPrintsTheReplyAndNotTheEcho(t *testing.T) {
	fi := newFakeInstance(t, []string{"solo/inbox"})
	s := fi.session(t, "", "talk")
	out := captureStdout(t, func() {
		if code := s.turn(context.Background(), "what broke?"); code != 0 {
			t.Errorf("turn = %d", code)
		}
	})
	if !strings.Contains(out, "the disk filled up") {
		t.Errorf("the agent's reply never printed; got:\n%s", out)
	}
	if strings.Contains(out, "ECHO OF MY OWN PROMPT") {
		t.Errorf("the caller's own message printed back at them; got:\n%s", out)
	}
}
