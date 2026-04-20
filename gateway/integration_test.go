package gateway

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/groupfolder"
	"github.com/onvos/arizuko/store"
	"github.com/onvos/arizuko/tests/testutils"
)

// fakeRunner implements container.Runner for gateway integration tests.
// It records inputs and returns a pre-programmed output; it also invokes
// the streaming OnOutput callback when StreamText is non-empty, mimicking
// the real runner's marker-parsing streaming behavior.
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
	if f.StreamText != "" && in.OnOutput != nil {
		in.OnOutput(f.StreamText, "success")
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
	grp := core.Group{Folder: "grp", Name: "Group"}
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
// caller can react, and the chat must be marked errored so the queue
// stops retrying until a new message arrives.
func TestPollLoop_RunnerError(t *testing.T) {
	gw, s, _, runner := newGWWithFake(t)
	runner.OutError = "agent blew up"

	jid := "tg:99"
	s.PutGroup(core.Group{Folder: "grp", Name: "G"})
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
	if !s.IsChatErrored(jid) {
		t.Error("chat should be marked errored after runner failure with no output")
	}
}

// TestPollLoop_EmptyOutput: runner produces no streamed output and no
// error. Gateway must handle gracefully — no panic, no sends, cursor
// still advances past the consumed message.
func TestPollLoop_EmptyOutput(t *testing.T) {
	gw, s, ch, runner := newGWWithFake(t)
	runner.OutStatus = "success" // no stream, no error

	jid := "tg:7"
	s.PutGroup(core.Group{Folder: "grp", Name: "G"})
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

// ensure fakeRunner satisfies the interface at compile-time.
var _ container.Runner = (*fakeRunner)(nil)

// placate unused helper when tests are trimmed
var _ = fmt.Sprintf
