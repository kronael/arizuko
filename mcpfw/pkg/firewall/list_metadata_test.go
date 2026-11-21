package firewall

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/mcpfw/pkg/rule"
)

func TestFilterToolsPreservesMetadata(t *testing.T) {
	f := Firewall{Rules: rule.Set{Rules: []rule.Rule{{Tool: "read", Action: rule.Allow}}}}
	reply := json.RawMessage(`{"jsonrpc":"2.0","id":1,"extension":true,"result":{
		"tools":[{"name":"read","extension":"keep"},{"name":"write"}],
		"nextCursor":"page2","_meta":{"tag":"a"},"extension":9007199254740993}}`)
	b, err := f.filterTools(reply)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(env["result"], &result); err != nil {
		t.Fatal(err)
	}
	if string(env["extension"]) != "true" || string(result["nextCursor"]) != `"page2"` ||
		string(result["_meta"]) != `{"tag":"a"}` || string(result["extension"]) != "9007199254740993" ||
		string(result["tools"]) != `[{"name":"read","extension":"keep"}]` {
		t.Fatalf("filtered response = %s", b)
	}
}
