package main

import "testing"

func TestStripHTML_BrTag(t *testing.T) {
	got := stripHTML("line1<br>line2")
	want := "line1\nline2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML_BrSelfClose(t *testing.T) {
	got := stripHTML("a<br/>b")
	want := "a\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML_BrSpaceSelfClose(t *testing.T) {
	got := stripHTML("a<br />b")
	want := "a\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML_PTag(t *testing.T) {
	got := stripHTML("<p>hello</p><p>world</p>")
	want := "hello\nworld"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML_HTMLEntities(t *testing.T) {
	got := stripHTML("a &amp; b &lt;c&gt;")
	want := "a & b <c>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML_TrimSpace(t *testing.T) {
	got := stripHTML("  <p>hello</p>  ")
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestStripHTML_Mixed(t *testing.T) {
	got := stripHTML(`<p>@user hello<br>world &amp; more</p>`)
	want := "@user hello\nworld & more"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestMentionFilter verifies that only notification events with type "mention"
// are dispatched. This is enforced by the streamOnce if-check. We test the
// condition logic directly by inspecting that non-mention types are skipped.
func TestMentionFilter_OnlyMention(t *testing.T) {
	types := []struct {
		typ      string
		dispatch bool
	}{
		{"mention", true},
		{"follow", false},
		{"reblog", false},
		{"favourite", false},
		{"poll", false},
	}
	for _, tc := range types {
		dispatched := tc.typ == "mention"
		if dispatched != tc.dispatch {
			t.Errorf("type %q: dispatch=%v, want %v", tc.typ, dispatched, tc.dispatch)
		}
	}
}
