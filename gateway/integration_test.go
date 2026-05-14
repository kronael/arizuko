package gateway

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// fakeRunner implements container.Runner for gateway integration tests.
// It records inputs and returns a pre-programmed output; when StreamText
// is non-empty it invokes the gateway's SubmitTurn callback via the
// in-process GatedFns hook, mimicking the real agent submitting a turn.
type fakeRunner struct {
	mu         sync.Mutex
	gotInputs  []container.Input
	StreamText string
	OutStatus  string
	OutError   string
}

func (f *fakeRunner) Run(
	_ *core.Config, _ *groupfolder.Resolver, in container.Input,
) container.Output {
	f.mu.Lock()
	f.gotInputs = append(f.gotInputs, in)
	f.mu.Unlock()

	if f.OutError != "" {
		return container.Output{Status: "error", Error: f.OutError}
	}
	if f.StreamText != "" && in.GatedFns.SubmitTurn != nil {
		in.GatedFns.SubmitTurn(in.Folder, ipc.TurnResult{
			TurnID:    in.MessageID,
			SessionID: "fake-sess",
			Status:    "success",
			Result:    f.StreamText,
		})
		return container.Output{Status: "success", HadOutput: true}
	}
	status := f.OutStatus
	if status == "" {
		status = "success"
	}
	return container.Output{Status: status, HadOutput: false}
}

func (f *fakeRunner) inputs() []container.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]container.Input, len(f.gotInputs))
	copy(cp, f.gotInputs)
	return cp
}

// newGWWithFake builds a Gateway wired to a fresh in-memory store, a
// FakeChannel for outbound capture, and a fakeRunner injected via
// SetRunner. The returned store is the same one the Gateway holds.
func newGWWithFake(t *testing.T) (
	*Gateway, *store.Store, *testutils.FakeChannel, *fakeRunner,
) {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	dir := t.TempDir()
	cfg := &core.Config{
		Name:          "test",
		MaxContainers: 2,
		IpcDir:        filepath.Join(dir, "ipc"),
		GroupsDir:     filepath.Join(dir, "groups"),
		ProjectRoot:   dir,
		Timezone:      "UTC",
		PollInterval:  time.Millisecond,
	}
	gw := New(cfg, s)
	runner := &fakeRunner{}
	gw.SetRunner(runner)
	ch := testutils.NewFakeChannel("fake", "tg:")
	gw.AddChannel(ch)
	return gw, s, ch, runner
}

// TestPollLoop_RealRun: insert a user message, run poll → process,
// FakeRunner receives the expected Input, the streamed output arrives at
// FakeChannel.Send, and the agent cursor advances past the message.
func TestPollLoop_RealRun(t *testing.T) {
	gw, s, ch, runner := newGWWithFake(t)
	runner.StreamText = "hello back"

	jid := "tg:42"
	grp := core.Group{Folder: "grp"}
	s.PutGroup(grp)
	s.AddRoute(core.Route{Seq: 0, Match: "room=42", Target: "grp"})

	ts := time.Now().UTC()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user", Name: "User",
		Content: "ping", Timestamp: ts,
	}); err != nil {
		t.Fatal(err)
	}

	// Drive poll → enqueue, then run the processor synchronously.
	gw.pollOnce()
	if _, err := gw.processGroupMessages(jid); err != nil {
		t.Fatalf("processGroupMessages: %v", err)
	}

	ins := runner.inputs()
	if len(ins) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(ins))
	}
	if ins[0].Folder != "grp" || ins[0].ChatJID != jid {
		t.Errorf("input = %+v", ins[0])
	}
	if ins[0].Prompt == "" {
		t.Error("runner got empty prompt")
	}

	sent := ch.Sent()
	if len(sent) != 1 {
		t.Fatalf("FakeChannel sent = %d, want 1", len(sent))
	}
	if sent[0].Text != "hello back" {
		t.Errorf("sent text = %q, want %q", sent[0].Text, "hello back")
	}

	cursor := s.GetAgentCursor(jid)
	if cursor.IsZero() || cursor.Before(ts) {
		t.Errorf("cursor = %v, want >= %v", cursor, ts)
	}
}

// TestPollLoop_RunnerError: runner returns an error with no streamed
// output. processGroupMessages must surface a failure so the queue's
// caller can react, and the offending message must be marked errored
// so it never feeds the agent again.
func TestPollLoop_RunnerError(t *testing.T) {
	gw, s, _, runner := newGWWithFake(t)
	runner.OutError = "agent blew up"

	jid := "tg:99"
	s.PutGroup(core.Group{Folder: "grp"})
	s.AddRoute(core.Route{Seq: 0, Match: "room=99", Target: "grp"})

	ts := time.Now().UTC()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user",
		Content: "boom", Timestamp: ts,
	}); err != nil {
		t.Fatal(err)
	}

	gw.pollOnce()
	ok, err := gw.processGroupMessages(jid)
	if err == nil || ok {
		t.Fatalf("processGroupMessages: ok=%v err=%v, want failure", ok, err)
	}
	// Failure semantics: cursor advances past the failed batch so the
	// poll loop doesn't replay it forever. The message is marked errored
	// in the DB but the cursor moved on.
	if s.HasPendingMessages(jid, gw.cfg.Name) {
		t.Error("errored message should not stay pending — cursor must advance to prevent infinite retry")
	}
	msgs, _ := s.MessagesSince(jid, time.Time{}, gw.cfg.Name)
	if len(msgs) != 1 || !msgs[0].Errored {
		t.Errorf("expected 1 errored-tagged message, got %+v", msgs)
	}
}

// TestPollLoop_EmptyOutput: runner produces no streamed output and no
// error. Gateway must handle gracefully — no panic, no sends, cursor
// still advances past the consumed message.
func TestPollLoop_EmptyOutput(t *testing.T) {
	gw, s, ch, runner := newGWWithFake(t)
	runner.OutStatus = "success" // no stream, no error

	jid := "tg:7"
	s.PutGroup(core.Group{Folder: "grp"})
	s.AddRoute(core.Route{Seq: 0, Match: "room=7", Target: "grp"})

	ts := time.Now().UTC()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user",
		Content: "quiet", Timestamp: ts,
	}); err != nil {
		t.Fatal(err)
	}

	gw.pollOnce()
	if _, err := gw.processGroupMessages(jid); err != nil {
		t.Fatalf("processGroupMessages: %v", err)
	}

	if sent := ch.Sent(); len(sent) != 0 {
		t.Errorf("FakeChannel sent = %d, want 0 (agent emitted nothing)", len(sent))
	}
	if got := runner.inputs(); len(got) != 1 {
		t.Fatalf("runner called %d times, want 1", len(got))
	}
	cursor := s.GetAgentCursor(jid)
	if cursor.IsZero() || cursor.Before(ts) {
		t.Errorf("cursor = %v, want >= %v", cursor, ts)
	}
}

// TestContainerNameIncludesInstance verifies that the container name passed to
// the runner embeds the instance name (cfg.Name), so CleanupOrphans can match
// it on restart with the "arizuko-<instance>-" prefix.
func TestContainerNameIncludesInstance(t *testing.T) {
	gw, s, _, runner := newGWWithFake(t)

	jid := "tg:99"
	grp := core.Group{Folder: "content"}
	s.PutGroup(grp)
	s.AddRoute(core.Route{Seq: 0, Match: "room=99", Target: "content"})

	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user", Name: "User",
		Content: "hello", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	gw.pollOnce()
	if _, err := gw.processGroupMessages(jid); err != nil {
		t.Fatalf("processGroupMessages: %v", err)
	}

	ins := runner.inputs()
	if len(ins) == 0 {
		t.Fatal("runner was not called")
	}
	name := ins[0].Name
	wantPrefix := "arizuko-" + gw.cfg.Name + "-"
	if !strings.HasPrefix(name, wantPrefix) {
		t.Errorf("container name %q does not start with %q — CleanupOrphans will miss it on restart", name, wantPrefix)
	}
}

// TestCursorDoesNotSkipBatchOnSteeredMessages verifies that steered messages
// (arriving while a container runs) do not advance the cursor past messages
// that were part of the original batch. After a successful run the cursor
// must be ≥ the latest message timestamp.
func TestCursorDoesNotSkipBatchOnSteeredMessages(t *testing.T) {
	gw, s, _, runner := newGWWithFake(t)
	runner.StreamText = "ok"

	jid := "tg:77"
	grp := core.Group{Folder: "grp2"}
	s.PutGroup(grp)
	s.AddRoute(core.Route{Seq: 0, Match: "room=77", Target: "grp2"})

	t0 := time.Now().UTC()
	t1 := t0.Add(time.Second)

	for _, m := range []core.Message{
		{ID: "a", ChatJID: jid, Sender: "user", Content: "first", Timestamp: t0},
		{ID: "b", ChatJID: jid, Sender: "user", Content: "second", Timestamp: t1},
	} {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	gw.pollOnce()
	if _, err := gw.processGroupMessages(jid); err != nil {
		t.Fatalf("processGroupMessages: %v", err)
	}

	cursor := s.GetAgentCursor(jid)
	if cursor.Before(t1) {
		t.Errorf("cursor %v is before latest message %v — earlier messages could be re-queued", cursor, t1)
	}
}

// ensure fakeRunner satisfies the interface at compile-time.
var _ container.Runner = (*fakeRunner)(nil)

// placate unused helper when tests are trimmed
var _ = fmt.Sprintf
