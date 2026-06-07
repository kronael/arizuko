// Package check implements anteval's public-surface checkers. Every checker
// asserts an externally observable effect — an HTTP status, a callback the
// agent's artifact made, or a message visible via REST/MCP — never the agent's
// prose and never the instance's internal state.
package check

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/anteval/pkg/spec"
)

// Hit is one callback the agent's artifact made to the harness sink.
type Hit struct {
	Query map[string]string
	Body  string
}

// Msg is one chat message seen through a public surface. FromBot separates the
// agent's own output from the harness-injected prompt, so rest_reply cannot
// pass on the marker the harness itself sent.
type Msg struct {
	FromBot bool
	Text    string
}

// Sink queries the harness callback sink for hits carrying a nonce.
type Sink interface {
	Hits(nonce string) []Hit
}

// Reader reads a target chat through the public REST and MCP surfaces (the two
// faces of the uniform surface, spec 5/5).
type Reader interface {
	RestMessages(chat string) ([]Msg, error)
	McpMessages(chat string) ([]Msg, error)
}

// Ctx is what a checker needs: HTTP for URL probes, the sink, a chat reader,
// and the per-run template expander.
type Ctx struct {
	HTTP   *http.Client
	Sink   Sink
	Reader Reader
	Expand func(string) string
}

// Run evaluates one check and returns pass plus a human-readable reason.
func Run(ctx Ctx, c spec.Check) (bool, string) {
	switch c.Kind {
	case "callback":
		n := ctx.Expand("{nonce}")
		if len(ctx.Sink.Hits(n)) > 0 {
			return true, "callback received for " + n
		}
		return false, "no callback for " + n
	case "http_status":
		url := ctx.Expand(c.URL)
		resp, err := ctx.HTTP.Get(url)
		if err != nil {
			return false, "GET " + url + ": " + err.Error()
		}
		resp.Body.Close()
		if resp.StatusCode == c.Want {
			return true, fmt.Sprintf("GET %s == %d", url, c.Want)
		}
		return false, fmt.Sprintf("GET %s == %d, want %d", url, resp.StatusCode, c.Want)
	case "rest_reply":
		return findText(ctx, c, false, true) // bot-authored only
	case "rest_observe":
		return findText(ctx, c, false, false)
	case "mcp_roundtrip":
		return findText(ctx, c, true, false)
	case "parity_sentinel":
		return parity(ctx, c)
	}
	return false, "unknown check kind " + c.Kind
}

func wantText(ctx Ctx, c spec.Check) string {
	t := c.Text
	if t == "" {
		t = "{nonce}"
	}
	return ctx.Expand(t)
}

func findText(ctx Ctx, c spec.Check, mcp, botOnly bool) (bool, string) {
	chat := ctx.Expand(c.Chat)
	want := wantText(ctx, c)
	var msgs []Msg
	var err error
	if mcp {
		msgs, err = ctx.Reader.McpMessages(chat)
	} else {
		msgs, err = ctx.Reader.RestMessages(chat)
	}
	if err != nil {
		return false, "read " + chat + ": " + err.Error()
	}
	if has(msgs, want, botOnly) {
		return true, "found " + want + " in " + chat
	}
	return false, want + " not yet in " + chat
}

func parity(ctx Ctx, c spec.Check) (bool, string) {
	chat := ctx.Expand(c.Chat)
	want := wantText(ctx, c)
	rest, err := ctx.Reader.RestMessages(chat)
	if err != nil {
		return false, "rest read: " + err.Error()
	}
	mcp, err := ctx.Reader.McpMessages(chat)
	if err != nil {
		return false, "mcp read: " + err.Error()
	}
	if has(rest, want, false) && has(mcp, want, false) {
		return true, "sentinel " + want + " visible via REST and MCP"
	}
	return false, fmt.Sprintf("parity miss: rest=%t mcp=%t", has(rest, want, false), has(mcp, want, false))
}

func has(msgs []Msg, want string, botOnly bool) bool {
	for _, m := range msgs {
		if botOnly && !m.FromBot {
			continue
		}
		if strings.Contains(m.Text, want) {
			return true
		}
	}
	return false
}
