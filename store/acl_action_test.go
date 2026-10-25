package store

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestACLActionEveryWriteDoor(t *testing.T) {
	s := openMem(t)
	doors := map[string]func(core.ACLRow) error{
		"AddACLRow": s.AddACLRow,
		"PutACLRow": s.PutACLRow,
		"ACLGrantWrite": func(row core.ACLRow) error {
			_, err := ACLGrantWrite(row)
			return err
		},
		"PutACLRowInto": func(row core.ACLRow) error {
			var ws WriteSet
			if err := s.PutACLRowInto(&ws, row); err != nil {
				if ws.Len() != 0 {
					t.Error("refused row buffered a write")
				}
				return err
			}
			return s.Apply(t.Context(), &ws)
		},
	}
	for name, write := range doors {
		t.Run(name, func(t *testing.T) {
			row := core.ACLRow{Principal: name, Scope: "team", Action: "send"}
			err := write(row)
			if err == nil || !strings.Contains(err.Error(), "action") ||
				!strings.Contains(err.Error(), "mcp:") {
				t.Errorf("bare send must explain the action namespace: %v", err)
			}
			if got := mustACL(s.ListACL(name)); len(got) != 0 {
				t.Errorf("refused action persisted: %+v", got)
			}
			for _, action := range []string{"mcp:send", "egress"} {
				row.Action = action
				if err := write(row); err != nil {
					t.Errorf("%s: %v", action, err)
				}
			}
		})
	}
}

func TestACLActionVocabulary(t *testing.T) {
	for _, action := range []string{
		"*", "admin", "interact", "egress", "web:publish", "gh:write", "mcp:*",
		"mcp:send", "shell:env:GITHUB_TOKEN", "shell:env:*",
		"ext:cloudflare:dns:read", "hold:mcp:send", "hold:mcp:*",
		"groups.list", "installed_packages:list", "installed_packages:get",
	} {
		t.Run(action, func(t *testing.T) {
			_, err := ACLGrantWrite(core.ACLRow{
				Principal: "alice", Scope: "team", Action: action,
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
