package routd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// Tier-2 parity: prompt.go rendering shipped but under-tested vs gated. Mirrors
// the gated autocalls / persona / prompt-envelope / previous_session asserts.

// promptLoop builds a Loop with a groups dir for persona/prompt assertions.
func promptLoop(t *testing.T) (*DB, *Loop, string) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	groups := t.TempDir()
	l := NewLoop(db, &recRunner{}, LoopConfig{GroupsDir: groups})
	l.StopQueue()
	return db, l, groups
}

// --- <autocalls> render (mirror autocalls_test.go:TestRenderAutocalls) ---

func TestRenderAutocalls(t *testing.T) {
	now := time.Date(2026, 4, 22, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		ctx     autocallCtx
		want    []string
		notWant []string
	}{
		{
			name: "all fields",
			ctx:  autocallCtx{Instance: "krons", Folder: "mayai", SessionID: "abcdef1234567", Tier: 2, Now: now},
			want: []string{
				"<autocalls>", "now: 2026-04-22T14:30:00Z", "instance: krons",
				"folder: mayai", "tier: 2", "session: abcdef12", "</autocalls>",
			},
		},
		{
			name:    "empty session skipped",
			ctx:     autocallCtx{Instance: "krons", Folder: "root", Tier: 0, Now: now},
			want:    []string{"now: 2026-04-22T14:30:00Z", "instance: krons", "folder: root", "tier: 0"},
			notWant: []string{"session:"},
		},
		{
			name:    "empty instance and folder skipped",
			ctx:     autocallCtx{Tier: 3, Now: now},
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

// --- strict <persona> block 4-case (mirror persona_test.go:TestPersonaBlock_*) ---

func writePersona(t *testing.T, groups, folder, body string) {
	t.Helper()
	dir := filepath.Join(groups, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPersonaBlock_NoFile: missing PERSONA.md → "" (gated TestPersonaBlock_NoFile).
func TestPersonaBlock_NoFile(t *testing.T) {
	_, l, _ := promptLoop(t)
	if got := l.personaBlock("nogroup"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestPersonaBlock_NoFrontmatter: a PERSONA.md without frontmatter → "" — no
// fallback to body text (gated TestPersonaBlock_NoFrontmatter).
func TestPersonaBlock_NoFrontmatter(t *testing.T) {
	_, l, groups := promptLoop(t)
	writePersona(t, groups, "g1", "# Persona\nno frontmatter at all\n")
	if got := l.personaBlock("g1"); got != "" {
		t.Errorf("expected empty (no frontmatter), got %q", got)
	}
}

// TestPersonaBlock_NoSummary: frontmatter without a summary field → "" (gated
// TestPersonaBlock_NoSummary).
func TestPersonaBlock_NoSummary(t *testing.T) {
	_, l, groups := promptLoop(t)
	writePersona(t, groups, "g1", "---\nname: Atlas\n---\n# body\n")
	if got := l.personaBlock("g1"); got != "" {
		t.Errorf("expected empty (no summary field), got %q", got)
	}
}

// TestPersonaBlock_WithSummary: full frontmatter renders the <persona> block with
// the name, summary, and pointer trailer (gated TestPersonaBlock_WithSummary).
func TestPersonaBlock_WithSummary(t *testing.T) {
	_, l, groups := promptLoop(t)
	writePersona(t, groups, "atlas", `---
name: Atlas
summary: |
  Codebase-native marinade guide. Dry, punchy.
  Cites paths. Refuses to guess.
---
# body
`)
	got := l.personaBlock("atlas")
	if !strings.Contains(got, `<persona name="Atlas">`) {
		t.Errorf("missing opening tag: %q", got)
	}
	if !strings.Contains(got, "Codebase-native marinade guide") {
		t.Errorf("missing summary content: %q", got)
	}
	if !strings.Contains(got, "(For full register: /persona)") {
		t.Errorf("missing pointer trailer: %q", got)
	}
	if !strings.HasSuffix(got, "</persona>\n") {
		t.Errorf("missing closing tag: %q", got)
	}
}

// --- <topic> envelope (mirror gateway_test.go:TestBuildAgentPrompt_TopicEnvelope) ---

// TestBuildAgentPrompt_TopicEnvelope: the prompt carries <topic name="#deploy" />
// for a thread and <topic name="" /> for the root, and never an <inherited>
// block (gated TestBuildAgentPrompt_TopicEnvelope).
func TestBuildAgentPrompt_TopicEnvelope(t *testing.T) {
	db, l, _ := promptLoop(t)
	_ = db.PutGroup(core.Group{Folder: "main"})
	now := time.Now().UTC()
	trigger := []core.Message{{ID: "t1", ChatJID: "tg:1", Sender: "alice",
		Content: "hey", Timestamp: now, Verb: "message"}}

	got := l.buildAgentPrompt("main", "#deploy", trigger)
	if !strings.Contains(got, `<topic name="#deploy" />`) {
		t.Errorf("missing <topic> envelope for #deploy; prompt:\n%s", got)
	}
	if strings.Contains(got, "<inherited") {
		t.Errorf("<inherited> block must not appear; prompt:\n%s", got)
	}

	gotMain := l.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(gotMain, `<topic name="" />`) {
		t.Errorf("missing <topic name=\"\" /> envelope for main; prompt:\n%s", gotMain)
	}
}

// --- <previous_session> field-level (mirror gateway_test.go:TestPreviousSessionXML_*) ---

// TestPreviousSessionXML_WithRecord: a completed session_log row renders the tag
// with truncated id, msg count, and result (gated TestPreviousSessionXML_WithRecord).
func TestPreviousSessionXML_WithRecord(t *testing.T) {
	now := time.Now()
	ended := now.Add(time.Minute)
	rec := core.SessionRecord{SessionID: "abc123def456", StartedAt: now, EndedAt: &ended, MsgCount: 7, Result: "ok"}
	got := previousSessionXML([]core.SessionRecord{rec})
	if !strings.Contains(got, "previous_session") {
		t.Errorf("expected previous_session tag, got %q", got)
	}
	if !strings.Contains(got, `msgs="7"`) {
		t.Errorf("expected msgs=7, got %q", got)
	}
	if !strings.Contains(got, `result="ok"`) {
		t.Errorf("expected result=ok, got %q", got)
	}
	if !strings.Contains(got, `"abc123de"`) {
		t.Errorf("expected truncated session id, got %q", got)
	}
}

// TestPreviousSessionXML_NoEndedAt: an unfinished session renders ended="" (gated
// TestPreviousSessionXML_NoEndedAt). Empty input → "".
func TestPreviousSessionXML_NoEndedAt(t *testing.T) {
	rec := core.SessionRecord{SessionID: "xyz", StartedAt: time.Now(), Result: "ok"}
	got := previousSessionXML([]core.SessionRecord{rec})
	if !strings.Contains(got, "previous_session") {
		t.Errorf("expected previous_session tag, got %q", got)
	}
	if !strings.Contains(got, `ended=""`) {
		t.Errorf("expected empty ended, got %q", got)
	}
	if got := previousSessionXML(nil); got != "" {
		t.Errorf("empty input → %q, want empty", got)
	}
}
