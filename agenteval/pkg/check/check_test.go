package check

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/agenteval/pkg/spec"
)

type fakeSink struct{ hits map[string][]Hit }

func (f fakeSink) Hits(n string) []Hit { return f.hits[n] }

type fakeReader struct{ rest, mcp []Msg }

func (f fakeReader) RestMessages(string) ([]Msg, error) { return f.rest, nil }
func (f fakeReader) McpMessages(string) ([]Msg, error)  { return f.mcp, nil }

func exp(s string) string { return strings.ReplaceAll(s, "{nonce}", "N1") }

func TestHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx := Ctx{HTTP: http.DefaultClient, Expand: func(string) string { return srv.URL }}
	if ok, _ := Run(ctx, spec.Check{Kind: "http_status", URL: "{target}", Want: 200}); !ok {
		t.Fatal("want pass on 200")
	}
	if ok, _ := Run(ctx, spec.Check{Kind: "http_status", URL: "{target}", Want: 404}); ok {
		t.Fatal("want fail on 200!=404")
	}
}

func TestCallback(t *testing.T) {
	ctx := Ctx{Sink: fakeSink{hits: map[string][]Hit{"N1": {{}}}}, Expand: exp}
	if ok, _ := Run(ctx, spec.Check{Kind: "callback"}); !ok {
		t.Fatal("want pass with a hit")
	}
	empty := Ctx{Sink: fakeSink{hits: map[string][]Hit{}}, Expand: exp}
	if ok, _ := Run(empty, spec.Check{Kind: "callback"}); ok {
		t.Fatal("want fail with no hit")
	}
}

func TestRestObserve(t *testing.T) {
	ctx := Ctx{Reader: fakeReader{rest: []Msg{{Text: "hello N1 world"}}}, Expand: exp}
	if ok, _ := Run(ctx, spec.Check{Kind: "rest_observe", Chat: "c"}); !ok {
		t.Fatal("want pass")
	}
	miss := Ctx{Reader: fakeReader{rest: []Msg{{Text: "nope"}}}, Expand: exp}
	if ok, _ := Run(miss, spec.Check{Kind: "rest_observe", Chat: "c"}); ok {
		t.Fatal("want fail")
	}
}

// TestRestReplyIgnoresInjectedPrompt guards the false-positive: the harness's
// own injected prompt carries the marker, so rest_reply must pass ONLY on a
// bot-authored message, not the user-authored prompt.
func TestRestReplyIgnoresInjectedPrompt(t *testing.T) {
	injectedOnly := Ctx{Reader: fakeReader{rest: []Msg{{FromBot: false, Text: "reply with N1"}}}, Expand: exp}
	if ok, _ := Run(injectedOnly, spec.Check{Kind: "rest_reply", Chat: "c"}); ok {
		t.Fatal("rest_reply must not pass on the injected (user) prompt")
	}
	withReply := Ctx{Reader: fakeReader{rest: []Msg{
		{FromBot: false, Text: "reply with N1"},
		{FromBot: true, Text: "N1"},
	}}, Expand: exp}
	if ok, _ := Run(withReply, spec.Check{Kind: "rest_reply", Chat: "c"}); !ok {
		t.Fatal("rest_reply must pass on the bot reply")
	}
}

func TestParity(t *testing.T) {
	both := Ctx{Reader: fakeReader{rest: []Msg{{Text: "N1"}}, mcp: []Msg{{Text: "N1"}}}, Expand: exp}
	if ok, _ := Run(both, spec.Check{Kind: "parity_sentinel", Chat: "c"}); !ok {
		t.Fatal("want pass when both surfaces show it")
	}
	mcpMiss := Ctx{Reader: fakeReader{rest: []Msg{{Text: "N1"}}, mcp: []Msg{{Text: "x"}}}, Expand: exp}
	if ok, _ := Run(mcpMiss, spec.Check{Kind: "parity_sentinel", Chat: "c"}); ok {
		t.Fatal("want fail when MCP lacks the sentinel")
	}
}
