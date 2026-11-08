package ipc

import (
	"strings"
	"testing"
)

func TestGroupSendDescriptionsExplainTurnsAndProgress(t *testing.T) {
	tools := ListTools("root/child", func(string) bool { return true })
	for name, phrases := range map[string][]string{
		"send":  {"queues a turn", "not for progress", "return address", "reply", "<status>"},
		"reply": {"current conversation", "replyToId", "<status>"},
	} {
		tool := findTool(tools, name)
		if tool == nil {
			t.Fatalf("%s missing", name)
		}
		for _, phrase := range phrases {
			if !strings.Contains(tool.Description, phrase) {
				t.Errorf("%s description missing %q: %s", name, phrase, tool.Description)
			}
		}
	}
	for _, name := range []string{"delegate_group", "escalate_group"} {
		if findTool(tools, name) != nil {
			t.Errorf("removed tool %s is advertised", name)
		}
	}
}
