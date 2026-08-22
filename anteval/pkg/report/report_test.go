package report

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarizeAndRender(t *testing.T) {
	rs := []Result{
		{ID: "a", Dimension: "web", Pass: true, Tokens: 5},
		{ID: "b", Dimension: "web", Pass: false, Tokens: 3},
		{ID: "c", Dimension: "self", Pass: true, Tokens: 2},
	}
	s := Summarize(rs)
	if s.Total != 3 || s.Passed != 2 || s.Tokens != 10 {
		t.Fatalf("summary %+v", s)
	}
	if s.ByDim["web"] != [2]int{1, 2} {
		t.Fatalf("web dim %v", s.ByDim["web"])
	}
	if AllPassed(rs) {
		t.Fatal("AllPassed should be false")
	}
	md := Markdown(rs)
	if !strings.Contains(md, "2/3 passed") {
		t.Fatalf("md header missing: %s", md)
	}
	b, err := JSON(rs)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || doc["results"] == nil {
		t.Fatalf("json missing results: %v", err)
	}
}

// A settle warning must reach the reader: an unreported turn means the NEXT
// case may have started behind it, and a hidden note reads as a clean failure.
func TestMarkdownShowsSettleNote(t *testing.T) {
	md := Markdown([]Result{{ID: "a", Pass: true, Reason: "ok", Note: "turn t1 never recorded a cost"}})
	if !strings.Contains(md, "NOTE: turn t1 never recorded a cost") {
		t.Fatalf("note missing from report:\n%s", md)
	}
}
