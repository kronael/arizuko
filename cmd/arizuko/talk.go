package main

// arizuko talk — a terminal conversation with the agent, spec 6/26 gap 1.
//
//	arizuko talk                    REPL against the one chat
//	arizuko talk -p "what broke?"   one turn: send, print the reply, exit
//	arizuko talk -p                 same, prompt read from stdin
//	arizuko talk @work -p "…"       a named chat instead of the default
//	arizuko talk -t bugfix          a different thread, same chat
//
// `talk` is an HTTP CLIENT and nothing else. It imports no `store`, opens no
// sqlite file and takes no instance argument, which is what lets it point at a
// remote host from a laptop. Contrast `arizuko send` (operator-direct: writes
// the inbound straight into the DB) and `arizuko chat` (an interactive Claude
// Code on the ROOT MCP socket — an operator tool, not a conversation with the
// persona).
//
// Two surfaces, both on the PUBLIC proxyd URL:
//
//	POST <url>/mcp                            list_groups + send   (webd/mcp.go)
//	GET  <url>/me/chats/<folder>/<topic>/sse  the round            (webd/me.go)
//
// Why proxyd and never webd directly: webd.identified requires BOTH an
// X-User-Sub header and auth.ProxydTransit, so webd will not read a raw bearer
// itself. proxyd.tryAuth verifies the ES256 bearer against authd's JWKs and
// stamps the identity headers; a client reaching webd directly is anonymous.
//
// Why the /me SSE stream rather than the MCP get_history tool: it is the only
// identity-gated read that carries round_done — the only one that says when the
// turn is OVER. get_history would be a poll loop with no terminator.
//
// Which scopes the bearer needs: NONE. proxyd ignores the token's scope claim
// here — groupsForSub re-reads the subject's `acl` rows and stamps those as
// X-User-Groups, and webd's send/list_groups gate on auth.MatchGroups over that
// header. `token issue bearer --scope` is required by the mint command, not by
// this path; the grant on the --sub is what decides reach.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// defaultTalkThread is a FIXED literal, not a remembered value: there is no
// last-used cache to go stale and no "which thread am I in?" — the answer is
// always the one -t names, or this.
const defaultTalkThread = "talk"

const talkUsage = `usage: arizuko talk [@name] [-g|--group <folder>] [-t|--thread <name>]
                   [-p|--prompt [text]] [--url <url>] [--token <bearer>]

  @name          a chat named by a [name] section in ~/.config/arizuko/talk.toml
  -g, --group    which folder to talk to (default: the config's group, or the
                 only folder you can reach)
  -t, --thread   conversation thread (default: ` + defaultTalkThread + `)
  -p, --prompt   one turn instead of a REPL; with no text, reads stdin
  --url          override the configured base URL
  --token        override the configured bearer (note: NOT -t, which is --thread)`

type talkFlags struct {
	Section string
	Group   string
	Thread  string
	URL     string
	Token   string
	// OneShot is -p. Prompt is its inline text; empty means read stdin.
	OneShot bool
	Prompt  string
}

// parseTalkArgs hand-rolls the parse because -p takes an OPTIONAL value, which
// the flag package cannot express. Two sigils, no collision: @ picks the chat,
// -t picks the thread.
func parseTalkArgs(args []string) (talkFlags, error) {
	f := talkFlags{Thread: defaultTalkThread}
	need := func(i int, what string) (string, error) {
		if i >= len(args) {
			return "", fmt.Errorf("%s needs a value", what)
		}
		return args[i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch {
		case a == "-h" || a == "--help":
			return f, errors.New(talkUsage)
		case a == "-p" || a == "--prompt":
			f.OneShot = true
			// Consume the next arg as the prompt unless it is another flag.
			// "-p" with nothing after it reads stdin instead.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				f.Prompt = args[i]
			}
		case a == "-g" || a == "--group":
			i++
			f.Group, err = need(i, a)
		case a == "-t" || a == "--thread":
			i++
			f.Thread, err = need(i, a)
		case a == "--url":
			i++
			f.URL, err = need(i, a)
		case a == "--token":
			i++
			f.Token, err = need(i, a)
		case strings.HasPrefix(a, "@"):
			if f.Section != "" {
				return f, fmt.Errorf("two chats named: @%s and %s", f.Section, a)
			}
			f.Section = strings.TrimPrefix(a, "@")
			if f.Section == "" {
				return f, errors.New("@ needs a name")
			}
		default:
			return f, fmt.Errorf("unexpected argument %q\n\n%s", a, talkUsage)
		}
		if err != nil {
			return f, err
		}
	}
	if f.Thread == "" {
		return f, errors.New("--thread needs a name")
	}
	return f, nil
}

// statusTripper records the last HTTP status the MCP transport saw. proxyd
// answers a bad bearer with a 303 to /auth/login, never a 401, so without this
// an expired token surfaces as "request failed with status 303: <!doctype html".
type statusTripper struct {
	base http.RoundTripper
	last atomic.Int32
}

func (t *statusTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if resp != nil {
		t.last.Store(int32(resp.StatusCode))
	}
	return resp, err
}

// talkSession holds the two clients for one run. The MCP session is opened once
// and reused: webd serves /mcp stateless, so every call is an independent POST.
type talkSession struct {
	entry  talkEntry
	thread string
	folder string
	mcp    *mcpclient.Client
	http   *http.Client
	status *statusTripper
	out    io.Writer
}

// talkHTTPClient never follows redirects: proxyd's answer to a rejected bearer
// is a 303 to /auth/login, and following it turns an auth failure into an HTML
// page and a parse error.
func talkHTTPClient(timeout time.Duration) (*http.Client, *statusTripper) {
	st := &statusTripper{base: http.DefaultTransport}
	return &http.Client{
		Timeout:       timeout,
		Transport:     st,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, st
}

func newTalkSession(ctx context.Context, e talkEntry, thread string, out io.Writer) (*talkSession, error) {
	// The MCP client's own timeout bounds one tool call; the SSE read uses a
	// separate client with no timeout, because a round legitimately runs for
	// minutes and a deadline there would cut the reply off mid-turn.
	callHTTP, callStatus := talkHTTPClient(60 * time.Second)
	c, err := mcpclient.NewStreamableHttpClient(e.URL+"/mcp",
		transport.WithHTTPBasicClient(callHTTP),
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + e.Token}),
	)
	if err != nil {
		return nil, fmt.Errorf("mcp client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp start: %w", err)
	}
	var init mcp.InitializeRequest
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "arizuko-talk", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = c.Close()
		return nil, talkAuthError(e.URL, callStatus.last.Load(), err)
	}
	streamHTTP, _ := talkHTTPClient(0)
	return &talkSession{
		entry: e, thread: thread, mcp: c,
		http: streamHTTP, status: callStatus, out: out,
	}, nil
}

func (s *talkSession) close() {
	if s.mcp != nil {
		_ = s.mcp.Close()
	}
}

// talkAuthError translates the two statuses that mean "your token is not good
// here" into a sentence naming the fix, per fail-loud-to-the-user.
func talkAuthError(base string, status int32, err error) error {
	switch status {
	case http.StatusSeeOther, http.StatusFound, http.StatusTemporaryRedirect:
		return fmt.Errorf("%s rejected the bearer (redirected to sign-in): the token is expired, "+
			"revoked, or minted by another instance — mint a new one with `arizuko token <inst> issue bearer`", base)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s refused the bearer (%d): check the token and its subject's grants", base, status)
	}
	return err
}

// callText runs one tool and returns its text payload.
func (s *talkSession) callText(ctx context.Context, tool string, args map[string]any) (string, error) {
	var req mcp.CallToolRequest
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := s.mcp.CallTool(ctx, req)
	if err != nil {
		return "", talkAuthError(s.entry.URL, s.status.last.Load(), fmt.Errorf("%s: %w", tool, err))
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		return "", fmt.Errorf("%s: %s", tool, strings.TrimSpace(text))
	}
	return text, nil
}

// resolveFolder settles the destination: -g or a config `group` wins; otherwise
// the folder is inferred ONLY when the caller can reach exactly one. That is
// what makes "one chat per user takes no arguments" true without hardcoding a
// default folder nobody has.
func (s *talkSession) resolveFolder(ctx context.Context) error {
	if s.entry.Group != "" {
		s.folder = s.entry.Group
		return nil
	}
	text, err := s.callText(ctx, "list_groups", map[string]any{})
	if err != nil {
		return err
	}
	var out struct {
		Groups []struct {
			Folder string `json:"folder"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return fmt.Errorf("list_groups: %w", err)
	}
	folders := make([]string, 0, len(out.Groups))
	for _, g := range out.Groups {
		folders = append(folders, g.Folder)
	}
	sort.Strings(folders)
	switch len(folders) {
	case 0:
		return fmt.Errorf("this token's subject can reach no folder — grant it one:\n" +
			"    arizuko group <instance> grant <sub> '<folder>/**'")
	case 1:
		s.folder = folders[0]
		return nil
	}
	return fmt.Errorf("this token reaches %d folders, so -g|--group is required:\n    %s",
		len(folders), strings.Join(folders, "\n    "))
}

// sseURL escapes each folder SEGMENT separately rather than the folder as one
// path element: webd matches /me/chats/{folder...} on the remaining segments,
// so a folder collapsed into one %2F-bearing element depends on how the mux
// treats an encoded slash. Segment-wise escaping has no such question.
func (s *talkSession) sseURL() string {
	parts := strings.Split(s.folder, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return fmt.Sprintf("%s/me/chats/%s/%s/sse",
		s.entry.URL, strings.Join(parts, "/"), url.PathEscape(s.thread))
}

// turn is ONE iteration of the REPL loop: subscribe, send, print the round.
// `-p` runs it exactly once. There is no second send path and no second
// printer — a one-shot form that drifted from the interactive one is the
// failure spec 6/26 names when it calls talk "the third consumer of one
// surface, never a fourth path into the queue".
//
// Subscribe BEFORE send: the /me stream replays nothing, so a reply that landed
// between the two would be lost and the round would never appear to end.
func (s *talkSession) turn(ctx context.Context, prompt string) int {
	resp, code := openSSE(s.http, s.sseURL(), map[string]string{"Authorization": "Bearer " + s.entry.Token})
	if resp == nil {
		return code
	}
	defer func() { _ = resp.Body.Close() }()

	if _, err := s.callText(ctx, "send", map[string]any{
		"folder": s.folder, "content": prompt, "topic": s.thread,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		return 2
	}
	return readSSE(resp.Body)
}

// promptSource yields prompts one at a time; ok=false ends the loop.
type promptSource func() (string, bool)

// onePrompt is the -p source: the loop runs exactly one iteration.
func onePrompt(text string) promptSource {
	done := false
	return func() (string, bool) {
		if done {
			return "", false
		}
		done = true
		return text, true
	}
}

// linePrompts is the REPL source: one line per turn until EOF.
func linePrompts(in io.Reader, out io.Writer) promptSource {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	return func() (string, bool) {
		_, _ = fmt.Fprint(out, "> ")
		if !scan.Scan() {
			_, _ = fmt.Fprintln(out)
			return "", false
		}
		return scan.Text(), true
	}
}

// talkPrompts picks the reader for this invocation. `-p` with no inline text
// reads the WHOLE of stdin as one prompt (a heredoc, a pipe); the REPL reads a
// line at a time. Only the READER differs — the send and the printer below are
// the same code for both.
func talkPrompts(f talkFlags, in io.Reader, out io.Writer) (promptSource, error) {
	if !f.OneShot {
		return linePrompts(in, out), nil
	}
	prompt := f.Prompt
	if prompt == "" {
		buf, err := io.ReadAll(in)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		prompt = string(buf)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("-p needs a prompt (inline or on stdin)")
	}
	return onePrompt(prompt), nil
}

func talkLoop(ctx context.Context, s *talkSession, next promptSource) int {
	code := 0
	for {
		line, ok := next()
		if !ok {
			return code
		}
		line = strings.TrimSpace(line)
		switch line {
		case "":
			continue
		case "/quit", "/exit":
			return code
		}
		code = s.turn(ctx, line)
	}
}

func cmdTalk(args []string) {
	f, err := parseTalkArgs(args)
	if err != nil {
		die("%v", err)
	}
	cfgPath := talkConfigPath()
	top, named, found, err := loadTalkConfig(cfgPath)
	if err != nil {
		die("Failed: %v", err)
	}
	entry, err := resolveTalkEntry(f, top, named, found, os.Getenv, cfgPath)
	if err != nil {
		die("Failed: %v", err)
	}

	next, err := talkPrompts(f, os.Stdin, os.Stdout)
	if err != nil {
		die("Failed: %v", err)
	}

	ctx := context.Background()
	s, err := newTalkSession(ctx, entry, f.Thread, os.Stdout)
	if err != nil {
		die("Failed: %v", err)
	}
	defer s.close()
	if err := s.resolveFolder(ctx); err != nil {
		die("Failed: %v", err)
	}
	if !f.OneShot {
		_, _ = fmt.Fprintf(os.Stdout, "%s · %s · %s  (ctrl-d or /quit to leave)\n",
			entry.URL, s.folder, s.thread)
	}
	os.Exit(talkLoop(ctx, s, next))
}
