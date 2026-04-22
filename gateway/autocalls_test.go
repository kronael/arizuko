package gateway

import (
	"strings"
	"testing"
	"time"
)

func TestRenderAutocalls(t *testing.T) {
	now := time.Date(2026, 4, 22, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		ctx     AutocallCtx
		want    []string
		notWant []string
	}{
		{
			name: "all fields",
			ctx: AutocallCtx{
				Instance: "krons", Folder: "mayai", ChatJID: "tg:42",
				Topic: "", SessionID: "abcdef1234567", Tier: 2, Now: now,
			},
			want: []string{
				"<autocalls>",
				"now: 2026-04-22T14:30:00Z",
				"instance: krons",
				"folder: mayai",
				"tier: 2",
				"session: abcdef12",
				"</autocalls>",
			},
		},
		{
			name: "empty session skipped",
			ctx:  AutocallCtx{Instance: "krons", Folder: "root", Tier: 0, Now: now},
			want: []string{
				"now: 2026-04-22T14:30:00Z",
				"instance: krons",
				"folder: root",
				"tier: 0",
			},
			notWant: []string{"session:"},
		},
		{
			name:    "empty instance and folder skipped",
			ctx:     AutocallCtx{Tier: 3, Now: now},
			want:    []string{"now:", "tier: 3"},
			notWant: []string{"instance:", "folder:", "session:"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderAutocalls(tc.ctx)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("want %q in output, got:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unwanted %q in output, got:\n%s", w, got)
				}
			}
			if !strings.HasSuffix(got, "</autocalls>\n") {
				t.Errorf("missing trailing newline after close tag:\n%s", got)
			}
		})
	}
}
