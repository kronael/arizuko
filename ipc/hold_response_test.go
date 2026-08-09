package ipc

import (
	"strings"
	"testing"
)

// TestHoldResponse_PendingIDIsTheContract — a recorded hold is a RESULT the
// agent reports and moves on from; an unrecordable one (empty id, BUGS J3) is an
// ERROR. Returning the pending shape without an id told the agent to wait for
// `/approve ` on nothing, which no operator could ever resolve.
func TestHoldResponse_PendingIDIsTheContract(t *testing.T) {
	held := holdResponse(7, "ab12", "mcp:delete")
	if held["error"] != nil {
		t.Fatalf("a recorded hold is not a failure: %+v", held)
	}
	res, ok := held["result"].(map[string]any)
	if !ok || res["pending"] != true || res["id"] != "ab12" {
		t.Fatalf("a recorded hold must carry pending+id: %+v", held)
	}

	lost := holdResponse(7, "", "mcp:delete")
	if lost["result"] != nil {
		t.Fatalf("a hold with no id must not look like a pending result: %+v", lost)
	}
	e, ok := lost["error"].(map[string]any)
	if !ok {
		t.Fatalf("a hold with no id must be a JSON-RPC error: %+v", lost)
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "mcp:delete") || strings.Contains(msg, "/approve") {
		t.Fatalf("error must name the tool and offer no id to approve: %q", msg)
	}
}
