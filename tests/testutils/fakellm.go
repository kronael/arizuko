package testutils

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// LLMReply is what the fake model answers every request with until replaced.
// Status >= 400 answers an Anthropic error envelope carrying Message instead
// of a message; the harness then reports the API failure.
type LLMReply struct {
	Text         string
	InputTokens  int
	OutputTokens int
	Status       int
	Message      string
}

// LLMCall is one request the fake model served, as much of it as a test asserts
// on: which model the harness asked for, the bearer it presented, and how many
// tools it advertised.
type LLMCall struct {
	Path   string
	Model  string
	Stream bool
	Bearer string
	Tools  int
}

// FakeLLM is the Anthropic Messages API stood in by a test: it speaks the wire
// shape the claude CLI expects — SSE when the request streams, one JSON message
// otherwise — and records every call. It listens on every interface so a
// container on the docker bridge reaches it through MODEL_GATEWAY_URL, which is
// the seam every harness already redirects on (spec 6/23 § Wiring); nothing in
// the agent path knows it is talking to a fake.
type FakeLLM struct {
	ln    net.Listener
	mu    sync.Mutex
	reply LLMReply
	calls []LLMCall
}

// NewFakeLLM starts the fake on an ephemeral port bound to 0.0.0.0 and closes
// it with the test.
func NewFakeLLM(t *testing.T, reply LLMReply) *FakeLLM {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("fake llm listen: %v", err)
	}
	f := &FakeLLM{ln: ln, reply: reply}
	srv := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return f
}

// Port is the bound TCP port.
func (f *FakeLLM) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// URLFor is the base URL a client on host's network uses to reach the fake —
// the docker bridge gateway for a container, 127.0.0.1 for the test process.
func (f *FakeLLM) URLFor(host string) string {
	return fmt.Sprintf("http://%s:%d", host, f.Port())
}

// SetReply replaces the scripted answer for every call from now on.
func (f *FakeLLM) SetReply(r LLMReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = r
}

// Calls returns a copy of every request served so far, in order.
func (f *FakeLLM) Calls() []LLMCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LLMCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *FakeLLM) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/messages") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model  string            `json:"model"`
		Stream bool              `json:"stream"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, LLMCall{
		Path: r.URL.Path, Model: body.Model, Stream: body.Stream,
		Bearer: r.Header.Get("Authorization"), Tools: len(body.Tools),
	})
	reply := f.reply
	f.mu.Unlock()

	if reply.Status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.Status)
		json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type": "invalid_request_error", "message": reply.Message,
			},
		})
		return
	}
	if body.Stream {
		f.stream(w, body.Model, reply)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": body.Model,
		"content":     []map[string]any{{"type": "text", "text": reply.Text}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens": reply.InputTokens, "output_tokens": reply.OutputTokens,
		},
	})
}

// stream writes the event sequence the SDK reduces into one assistant message:
// usage arrives split, input on message_start and output on message_delta,
// exactly where the client reads it.
func (f *FakeLLM) stream(w http.ResponseWriter, model string, reply LLMReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	ev := func(name string, data any) {
		raw, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		if flush != nil {
			flush.Flush()
		}
	}
	ev("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens": reply.InputTokens, "output_tokens": 1,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			},
		},
	})
	ev("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	ev("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": reply.Text},
	})
	ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	ev("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": reply.OutputTokens},
	})
	ev("message_stop", map[string]any{"type": "message_stop"})
}
