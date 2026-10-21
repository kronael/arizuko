package ipc

import (
	"fmt"
	"testing"
)

func TestSubmitStatusKindsUseTheExistingHiddenMethod(t *testing.T) {
	for _, kind := range []string{"", "summary", "progress", "other"} {
		t.Run(kind, func(t *testing.T) {
			var got TurnStatus
			called := false
			routd := RoutdFns{SubmitStatus: func(folder string, status TurnStatus) error {
				if folder != "demo" {
					t.Errorf("folder=%q", folder)
				}
				called, got = true, status
				return nil
			}}
			raw := []byte(fmt.Sprintf(`{"params":{"turn_id":"t1","text":"the task","kind":%q}}`, kind))
			var response any
			handleSubmitStatus(raw, 7, routd, "demo", func(v any) { response = v })
			if response == nil || called != (kind != "other") {
				t.Fatalf("called=%v response=%+v", called, response)
			}
			if called && (got.TurnID != "t1" || got.Text != "the task" || string(got.Kind) != kind) {
				t.Fatalf("status=%+v", got)
			}
		})
	}
}
