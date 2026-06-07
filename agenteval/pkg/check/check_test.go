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

type fakeReader struct{ rest, mcp []string }

func (f fakeReader) RestMessages(string) ([]string, error) { return f.rest, nil }
func (f fakeReader) McpMessages(string) ([]string, error)  { return f.mcp, nil }

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
	ctx := Ctx{Reader: fakeReader{rest: []string{"hello N1 world"}}, Expand: exp}
	if ok, _ := Run(ctx, spec.Check{Kind: "rest_observe", Chat: "c"}); !ok {
		t.Fatal("want pass")
	}
	miss := Ctx{Reader: fakeReader{rest: []string{"nope"}}, Expand: exp}
	if ok, _ := Run(miss, spec.Check{Kind: "rest_observe", Chat: "c"}); ok {
		t.Fatal("want fail")
	}
}

func TestParity(t *testing.T) {
	both := Ctx{Reader: fakeReader{rest: []string{"N1"}, mcp: []string{"N1"}}, Expand: exp}
	if ok, _ := Run(both, spec.Check{Kind: "parity_sentinel", Chat: "c"}); !ok {
		t.Fatal("want pass when both surfaces show it")
	}
	mcpMiss := Ctx{Reader: fakeReader{rest: []string{"N1"}, mcp: []string{"x"}}, Expand: exp}
	if ok, _ := Run(mcpMiss, spec.Check{Kind: "parity_sentinel", Chat: "c"}); ok {
		t.Fatal("want fail when MCP lacks the sentinel")
	}
}
