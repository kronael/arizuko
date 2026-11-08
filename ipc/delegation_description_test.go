package ipc

import (
	"strings"
	"testing"
)

func TestDelegationDescriptionsExplainTurnsAndProgress(t *testing.T) {
	tools := ListTools("root/child", func(string) bool { return true })
	for name, phrases := range map[string][]string{
		"escalate_group": {"queues a parent turn", "not for progress", "<status>"},
		"delegate_group": {"queues a child turn", "return address", "reply"},
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
}
