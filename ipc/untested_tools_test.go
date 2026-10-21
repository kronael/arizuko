package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// Twelve of the twenty-nine registered MCP tools were never named in any test:
// fork_topic, inject_message, observe_group, unobserve_group, pane_set_prompts,
// pane_set_title, post, reset_session, set_group_open, set_observe_window,
// get_work and set_work. The existing coverage stopped at "buildMCPServer
// returned non-nil", which does not run a single handler body.
//
// These are the agent's action surface. A handler that reads the wrong
// argument name gets the zero value and acts on it: fork a topic named "",
// open the wrong folder, set an empty observe window.

// serveTools starts a real MCP socket over the given hooks and returns its path.
func serveTools(t *testing.T, routd RoutdFns, db StoreFns, folder string) string {
	t.Helper()
	if routd.GroupsDir == "" {
		routd.GroupsDir = t.TempDir()
	}
	if db.DefaultFolderForJID == nil {
		db.DefaultFolderForJID = func(string) string { return folder }
	}
	sock := filepath.Join(t.TempDir(), "gated.sock")
	stop, err := ServeMCP(sock, routd, db, folder, true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	return sock
}

func mustSucceed(t *testing.T, raw, tool string) {
	t.Helper()
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("%s failed: %s", tool, raw)
	}
}

func mustFail(t *testing.T, raw, tool string) {
	t.Helper()
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("%s was expected to refuse, got: %s", tool, raw)
	}
}

// fork_topic

func TestForkTopicPassesParentChildAndForce(t *testing.T) {
	var gotFolder, gotParent, gotChild string
	var gotForce bool
	calls := 0
	sock := serveTools(t, RoutdFns{
		ForkTopic: func(folder, parent, child string, force bool) error {
			calls++
			gotFolder, gotParent, gotChild, gotForce = folder, parent, child, force
			return nil
		},
	}, StoreFns{}, "acme/eng")

	mustSucceed(t, callRaw(t, sock, "fork_topic", map[string]any{
		"parent": "design", "child": "design-api", "force": true,
	}), "fork_topic")

	if calls != 1 {
		t.Fatalf("ForkTopic called %d times, want 1", calls)
	}
	if gotFolder != "acme/eng" {
		t.Errorf("folder = %q, want the socket's own folder, never one the agent named", gotFolder)
	}
	if gotParent != "design" || gotChild != "design-api" {
		t.Errorf("parent/child = %q/%q", gotParent, gotChild)
	}
	if !gotForce {
		t.Error("force=true did not reach ForkTopic; an existing child would not be overwritten")
	}

	// force defaults to false — a fork must not clobber an existing topic
	// because the agent left the flag out.
	mustSucceed(t, callRaw(t, sock, "fork_topic", map[string]any{
		"parent": "design", "child": "design-ui",
	}), "fork_topic")
	if gotForce {
		t.Error("force defaulted to true; an omitted flag must not overwrite")
	}
}

func TestForkTopicRefusesAnEmptyChildAndAMissingHook(t *testing.T) {
	sock := serveTools(t, RoutdFns{
		ForkTopic: func(string, string, string, bool) error { return nil },
	}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "fork_topic", map[string]any{
		"parent": "design", "child": "",
	}), "fork_topic with an empty child")

	bare := serveTools(t, RoutdFns{}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, bare, "fork_topic", map[string]any{
		"parent": "design", "child": "x",
	}), "fork_topic with no hook")
}

// inject_message

func TestInjectMessageCarriesEveryField(t *testing.T) {
	var jid, content, sender, senderName string
	sock := serveTools(t, RoutdFns{
		InjectMessage: func(j, c, s, n string) (string, error) {
			jid, content, sender, senderName = j, c, s, n
			return "m-1", nil
		},
	}, StoreFns{}, "acme")

	mustSucceed(t, callRaw(t, sock, "inject_message", map[string]any{
		"chatJid": "slack:C1", "content": "hello", "sender": "u7", "senderName": "Alice",
	}), "inject_message")

	if jid != "slack:C1" || content != "hello" {
		t.Errorf("jid/content = %q/%q", jid, content)
	}
	if sender != "u7" || senderName != "Alice" {
		t.Errorf("sender/senderName = %q/%q — an injected message would be attributed to nobody", sender, senderName)
	}
}

func TestInjectMessageRefusesAnEmptyJID(t *testing.T) {
	called := false
	sock := serveTools(t, RoutdFns{
		InjectMessage: func(string, string, string, string) (string, error) {
			called = true
			return "", nil
		},
	}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "inject_message", map[string]any{
		"chatJid": "", "content": "hello",
	}), "inject_message with no jid")
	if called {
		t.Error("the hook ran for an empty chatJid")
	}
}

// observe_group / unobserve_group

// The observer is always the socket's own folder. Taking it from an argument
// would let an agent subscribe a DIFFERENT folder to its own inbound.
func TestObserveGroupBindsTheObserverToTheSocketFolder(t *testing.T) {
	var addObs, addSrc, delObs, delSrc string
	sock := serveTools(t, RoutdFns{
		AddGroupWatcher:    func(o, s string) error { addObs, addSrc = o, s; return nil },
		RemoveGroupWatcher: func(o, s string) error { delObs, delSrc = o, s; return nil },
	}, StoreFns{}, "acme/eng")

	// The extra arguments are the attack: a handler that reads any of them
	// instead of the socket's folder subscribes a folder the caller does not
	// hold to another folder's inbound.
	got, errText := callTool(t, sock, "observe_group", map[string]any{
		"source":   "acme/sales",
		"observer": "acme/finance",
		"folder":   "acme/finance",
		"target":   "acme/finance",
	})
	if errText != "" {
		t.Fatalf("observe_group: %s", errText)
	}
	if addObs != "acme/eng" || addSrc != "acme/sales" {
		t.Errorf("AddGroupWatcher(%q, %q), want (acme/eng, acme/sales)", addObs, addSrc)
	}
	if got["observer"] != "acme/eng" || got["source"] != "acme/sales" {
		t.Errorf("the result must report the pair it bound, got %#v", got)
	}

	mustSucceed(t, callRaw(t, sock, "unobserve_group", map[string]any{
		"source": "acme/sales", "observer": "acme/finance", "folder": "acme/finance",
	}), "unobserve_group")
	if delObs != "acme/eng" || delSrc != "acme/sales" {
		t.Errorf("RemoveGroupWatcher(%q, %q)", delObs, delSrc)
	}
}

func TestUnobserveGroupRefusesAnEmptySource(t *testing.T) {
	called := false
	sock := serveTools(t, RoutdFns{
		RemoveGroupWatcher: func(string, string) error { called = true; return nil },
	}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "unobserve_group", map[string]any{"source": ""}), "unobserve_group")
	if called {
		t.Error("the hook ran for an empty source")
	}
}

// pane_set_prompts / pane_set_title

func TestPaneSetPromptsDecodesTheTitleMessagePairs(t *testing.T) {
	var gotJID string
	var gotPrompts []core.PanePrompt
	sock := serveTools(t, RoutdFns{
		PaneSetPrompts: func(jid string, p []core.PanePrompt) error {
			gotJID, gotPrompts = jid, p
			return nil
		},
	}, StoreFns{}, "acme")

	mustSucceed(t, callRaw(t, sock, "pane_set_prompts", map[string]any{
		"chatJid": "slack:C1",
		"prompts": []any{
			map[string]any{"title": "Retry", "message": "try again"},
			map[string]any{"title": "Explain", "message": "why?"},
		},
	}), "pane_set_prompts")

	if gotJID != "slack:C1" {
		t.Errorf("jid = %q", gotJID)
	}
	if len(gotPrompts) != 2 {
		t.Fatalf("prompts = %#v, want two", gotPrompts)
	}
	if gotPrompts[0].Title != "Retry" || gotPrompts[0].Message != "try again" {
		t.Errorf("first prompt = %+v; a button with no message sends nothing on click", gotPrompts[0])
	}
	if gotPrompts[1].Title != "Explain" {
		t.Errorf("second prompt = %+v", gotPrompts[1])
	}
}

// The title is trimmed, must not be empty, and is length-bounded: an
// unbounded title is a platform API rejection the agent cannot diagnose.
func TestPaneSetTitleTrimsAndBoundsTheTitle(t *testing.T) {
	var gotTitle string
	calls := 0
	sock := serveTools(t, RoutdFns{
		PaneSetTitle: func(_, title string) error { calls++; gotTitle = title; return nil },
	}, StoreFns{}, "acme")

	mustSucceed(t, callRaw(t, sock, "pane_set_title", map[string]any{
		"chatJid": "slack:C1", "title": "  Design review  ",
	}), "pane_set_title")
	if gotTitle != "Design review" {
		t.Errorf("title = %q, want it trimmed", gotTitle)
	}

	before := calls
	for _, bad := range []string{"", "   ", strings.Repeat("x", 257)} {
		mustFail(t, callRaw(t, sock, "pane_set_title", map[string]any{
			"chatJid": "slack:C1", "title": bad,
		}), "pane_set_title with a bad title")
	}
	if calls != before {
		t.Errorf("the hook ran %d extra times for rejected titles", calls-before)
	}

	// 256 is the boundary and must be accepted.
	mustSucceed(t, callRaw(t, sock, "pane_set_title", map[string]any{
		"chatJid": "slack:C1", "title": strings.Repeat("x", 256),
	}), "pane_set_title at the limit")
}

// post

func TestPostSendsContentAndResolvesMediaInsideTheGroup(t *testing.T) {
	groups := t.TempDir()
	folder := "acme"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(groups, folder, "shot.png")
	if err := os.WriteFile(img, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotJID, gotContent string
	var gotMedia []string
	sock := serveTools(t, RoutdFns{
		GroupsDir: groups,
		Post: func(jid, content string, media []string) (string, error) {
			gotJID, gotContent, gotMedia = jid, content, media
			return "post-1", nil
		},
	}, StoreFns{}, folder)

	// The agent addresses files by their CONTAINER path; workspaceRel maps
	// core.ContainerHome onto the group's directory on the host.
	got, errText := callTool(t, sock, "post", map[string]any{
		"chatJid": "bsky:feed", "content": "hello world",
		"media": []any{core.ContainerHome + "/shot.png"},
	})
	if errText != "" {
		t.Fatalf("post: %s", errText)
	}
	if gotJID != "bsky:feed" || gotContent != "hello world" {
		t.Errorf("jid/content = %q/%q", gotJID, gotContent)
	}
	if len(gotMedia) != 1 || gotMedia[0] != img {
		t.Errorf("media = %#v, want the container path resolved to %s", gotMedia, img)
	}
	if got["id"] != "post-1" {
		t.Errorf("the platform id must come back for a later edit/delete: %#v", got)
	}
}

// A media path that escapes the group directory is the containment boundary:
// without it a post uploads any file the container can read.
func TestPostRefusesMediaOutsideTheGroupDirectory(t *testing.T) {
	groups := t.TempDir()
	if err := os.MkdirAll(filepath.Join(groups, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groups, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	sock := serveTools(t, RoutdFns{
		GroupsDir: groups,
		Post: func(string, string, []string) (string, error) {
			called = true
			return "", nil
		},
	}, StoreFns{}, "acme")

	for _, escape := range []string{
		core.ContainerHome + "/../secret.txt",
		core.ContainerHome + "/../../etc/passwd",
		"/etc/passwd",   // not under the container home at all
		"../secret.txt", // relative, so not under it either
	} {
		mustFail(t, callRaw(t, sock, "post", map[string]any{
			"chatJid": "bsky:feed", "content": "x", "media": []any{escape},
		}), "post with media path "+escape)
	}
	if called {
		t.Error("Post ran for a media path outside the group directory")
	}
}

func TestPostRefusesWithNoHook(t *testing.T) {
	sock := serveTools(t, RoutdFns{}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "post", map[string]any{
		"chatJid": "bsky:feed", "content": "x",
	}), "post with no hook")
}

// reset_session

func TestResetSessionClearsTheNamedFolder(t *testing.T) {
	var cleared string
	calls := 0
	sock := serveTools(t, RoutdFns{
		ClearSession: func(f string) { calls++; cleared = f },
	}, StoreFns{}, "acme")

	mustSucceed(t, callRaw(t, sock, "reset_session", map[string]any{"groupFolder": "acme"}), "reset_session")
	if cleared != "acme" {
		t.Errorf("ClearSession(%q), want acme", cleared)
	}

	before := calls
	mustFail(t, callRaw(t, sock, "reset_session", map[string]any{"groupFolder": ""}), "reset_session with no folder")
	if calls != before {
		t.Error("an empty groupFolder cleared a session anyway")
	}
}

// set_group_open

func TestSetGroupOpenCarriesBothTrueAndFalse(t *testing.T) {
	var gotFolder string
	var gotOpen bool
	calls := 0
	sock := serveTools(t, RoutdFns{
		SetGroupOpen: func(f string, open bool) error {
			calls++
			gotFolder, gotOpen = f, open
			return nil
		},
	}, StoreFns{}, "acme")

	got, errText := callTool(t, sock, "set_group_open", map[string]any{"open": true})
	if errText != "" {
		t.Fatalf("set_group_open: %s", errText)
	}
	if gotFolder != "acme" || !gotOpen {
		t.Errorf("SetGroupOpen(%q, %v), want (acme, true)", gotFolder, gotOpen)
	}
	if got["open"] != true || got["folder"] != "acme" {
		t.Errorf("the result must echo the state it set, got %#v", got)
	}

	// false is the dangerous one: dropped as a zero value it would silently
	// leave the group open.
	got, errText = callTool(t, sock, "set_group_open", map[string]any{"open": false})
	if errText != "" {
		t.Fatalf("set_group_open false: %s", errText)
	}
	if gotOpen {
		t.Error("open=false did not reach the hook; the group would stay open")
	}
	if got["open"] != false {
		t.Errorf("the result reported %#v for open=false", got["open"])
	}
	if calls != 2 {
		t.Errorf("hook ran %d times, want 2", calls)
	}
}

// set_observe_window

// The stored window has two independent halves, and the tool takes both as
// OPTIONAL. An absent half must be re-sent as the stored value, not as zero:
// setting only `messages` would otherwise silently clear `chars` to 0 and the
// group would observe nothing.
func TestSetObserveWindowPreservesTheHalfThatWasNotSent(t *testing.T) {
	var gotFolder string
	var gotMsgs, gotChars int
	sock := serveTools(t, RoutdFns{
		GroupObserveWindow: func(string) (int, int) { return 10, 500 },
		SetGroupObserveWindow: func(f string, m, c int) error {
			gotFolder, gotMsgs, gotChars = f, m, c
			return nil
		},
	}, StoreFns{}, "acme")

	got, errText := callTool(t, sock, "set_observe_window", map[string]any{
		"messages": 25, "chars": 2000,
	})
	if errText != "" {
		t.Fatalf("set_observe_window: %s", errText)
	}
	if gotFolder != "acme" || gotMsgs != 25 || gotChars != 2000 {
		t.Errorf("SetGroupObserveWindow(%q, %d, %d), want (acme, 25, 2000)", gotFolder, gotMsgs, gotChars)
	}
	if got["messages"] != float64(25) || got["chars"] != float64(2000) {
		t.Errorf("the result must echo what it stored, got %#v", got)
	}

	if _, errText = callTool(t, sock, "set_observe_window", map[string]any{"messages": 40}); errText != "" {
		t.Fatalf("messages-only call: %s", errText)
	}
	if gotMsgs != 40 || gotChars != 500 {
		t.Errorf("got (%d, %d); an absent `chars` must keep the stored 500, not clear it to 0", gotMsgs, gotChars)
	}

	if _, errText = callTool(t, sock, "set_observe_window", map[string]any{"chars": 300}); errText != "" {
		t.Fatalf("chars-only call: %s", errText)
	}
	if gotMsgs != 10 || gotChars != 300 {
		t.Errorf("got (%d, %d); an absent `messages` must keep the stored 10", gotMsgs, gotChars)
	}
}

// Neither half is a call with nothing to do; it must be refused rather than
// re-storing the current window and reading as a change.
func TestSetObserveWindowRefusesWithNeitherHalf(t *testing.T) {
	called := false
	sock := serveTools(t, RoutdFns{
		GroupObserveWindow:    func(string) (int, int) { return 10, 500 },
		SetGroupObserveWindow: func(string, int, int) error { called = true; return nil },
	}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "set_observe_window", map[string]any{}), "set_observe_window with no args")
	if called {
		t.Error("a call with neither half still wrote the window")
	}
}

func TestSetObserveWindowRefusesWithNoHook(t *testing.T) {
	sock := serveTools(t, RoutdFns{}, StoreFns{}, "acme")
	mustFail(t, callRaw(t, sock, "set_observe_window", map[string]any{
		"messages": 25, "chars": 2000,
	}), "set_observe_window with no hook")
}

// get_work / set_work

// work.md is how a turn recovers what was in flight. get_work must report a
// missing file as exists=false rather than failing — a fresh group has none.
func TestWorkRoundTripsThroughTheGroupDirectory(t *testing.T) {
	groups := t.TempDir()
	folder := "acme/eng"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := serveTools(t, RoutdFns{GroupsDir: groups}, StoreFns{}, folder)

	got, errText := callTool(t, sock, "get_work", map[string]any{})
	if errText != "" {
		t.Fatalf("get_work on a fresh group: %s", errText)
	}
	if got["exists"] != false || got["content"] != "" {
		t.Errorf("a missing work.md returned %#v, want exists=false content=\"\"", got)
	}

	mustSucceed(t, callRaw(t, sock, "set_work", map[string]any{
		"content": "## in flight\n- the migration\n",
	}), "set_work")

	onDisk, err := os.ReadFile(filepath.Join(groups, folder, "work.md"))
	if err != nil {
		t.Fatalf("set_work wrote nothing: %v", err)
	}
	if string(onDisk) != "## in flight\n- the migration\n" {
		t.Errorf("work.md = %q", onDisk)
	}

	got, errText = callTool(t, sock, "get_work", map[string]any{})
	if errText != "" {
		t.Fatalf("get_work after set_work: %s", errText)
	}
	if got["exists"] != true {
		t.Errorf("exists = %v after a write", got["exists"])
	}
	if got["content"] != "## in flight\n- the migration\n" {
		t.Errorf("content = %q", got["content"])
	}

	// set_work REPLACES rather than appends — the doc says so, and an agent
	// that expects append would otherwise lose the old snapshot silently.
	mustSucceed(t, callRaw(t, sock, "set_work", map[string]any{"content": "fresh"}), "set_work overwrite")
	onDisk, _ = os.ReadFile(filepath.Join(groups, folder, "work.md"))
	if string(onDisk) != "fresh" {
		t.Errorf("work.md = %q, want the overwrite", onDisk)
	}
}

// The path is composed from GroupsDir and the SOCKET's folder, never from an
// argument, so one group cannot read or overwrite another's work.md.
func TestWorkIsScopedToTheSocketFolder(t *testing.T) {
	groups := t.TempDir()
	for _, f := range []string{"acme/eng", "acme/sales"} {
		if err := os.MkdirAll(filepath.Join(groups, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(groups, "acme/sales", "work.md"), []byte("sales secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	sock := serveTools(t, RoutdFns{GroupsDir: groups}, StoreFns{}, "acme/eng")
	got, errText := callTool(t, sock, "get_work", map[string]any{"folder": "acme/sales", "groupFolder": "acme/sales"})
	if errText != "" {
		t.Fatalf("get_work: %s", errText)
	}
	if strings.Contains(toStr(got["content"]), "sales secret") {
		t.Fatalf("get_work read another folder's work.md: %#v", got)
	}

	mustSucceed(t, callRaw(t, sock, "set_work", map[string]any{
		"content": "eng only", "folder": "acme/sales",
	}), "set_work")
	sales, _ := os.ReadFile(filepath.Join(groups, "acme/sales", "work.md"))
	if string(sales) != "sales secret" {
		t.Errorf("set_work overwrote another folder's work.md: %q", sales)
	}
	eng, _ := os.ReadFile(filepath.Join(groups, "acme/eng", "work.md"))
	if string(eng) != "eng only" {
		t.Errorf("the write landed in %q instead of the caller's folder", eng)
	}
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}
