package firewall

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/mcpfw/pkg/rule"
)

// stub records whether upstream was reached — the acceptance turns on a denied
// call NOT touching it, so "reached" is the assertion, not the reply.
type stub struct {
	reached bool
	reply   string
}

func (s *stub) Call(json.RawMessage) (json.RawMessage, error) {
	s.reached = true
	if s.reply == "" {
		s.reply = `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	}
	return json.RawMessage(s.reply), nil
}

func readAllowSendDeny() rule.Set {
	return rule.Set{Rules: []rule.Rule{
		{Tool: "read_*", Action: rule.Allow},
		{Tool: "send_*", Action: rule.Deny},
	}}
}

func call(tool string) json.RawMessage {
	return json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `"}}`)
}

func TestAllowedCallReachesUpstream(t *testing.T) {
	up := &stub{}
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: up}
	out, err := f.Handle(call("read_file"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !up.reached {
		t.Error("allowed call did not reach upstream")
	}
	if !strings.Contains(string(out), `"result"`) {
		t.Errorf("expected upstream result, got %s", out)
	}
}

// TestDeniedCallNeverTouchesUpstream is the acceptance case: a refusal must be
// decided before the side effect, not after it.
func TestDeniedCallNeverTouchesUpstream(t *testing.T) {
	up := &stub{}
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: up}
	out, err := f.Handle(call("send_message"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if up.reached {
		t.Fatal("denied call reached upstream")
	}
	if !strings.Contains(string(out), `"error"`) {
		t.Errorf("expected a JSON-RPC error, got %s", out)
	}
}

func TestUnlistedToolIsDenied(t *testing.T) {
	up := &stub{}
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: up}
	out, _ := f.Handle(call("delete_everything"))
	if up.reached {
		t.Fatal("a tool matched by no rule reached upstream")
	}
	if !strings.Contains(string(out), `"error"`) {
		t.Errorf("expected default-deny error, got %s", out)
	}
}

func TestToolsListFilteringHidesDenied(t *testing.T) {
	up := &stub{reply: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"},{"name":"send_message"}]}}`}
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: up, FilterList: true}
	out, err := f.Handle(json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if strings.Contains(string(out), "send_message") {
		t.Errorf("denied tool was advertised: %s", out)
	}
	if !strings.Contains(string(out), "read_file") {
		t.Errorf("allowed tool was hidden: %s", out)
	}
}

func TestToolsListUnfilteredByDefault(t *testing.T) {
	up := &stub{reply: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"send_message"}]}}`}
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: up}
	out, _ := f.Handle(json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !strings.Contains(string(out), "send_message") {
		t.Error("listing was filtered without FilterList set")
	}
}

func TestLogSeesDeniedCall(t *testing.T) {
	var gotTool string
	var gotAllowed bool
	f := &Firewall{Rules: readAllowSendDeny(), Upstream: &stub{},
		Log: func(tool string, allowed bool) { gotTool, gotAllowed = tool, allowed }}
	f.Handle(call("send_message"))
	if gotTool != "send_message" || gotAllowed {
		t.Errorf("log got (%q, %v), want (send_message, false)", gotTool, gotAllowed)
	}
}
