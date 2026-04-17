package main

import "testing"

func TestStripHTML(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"br tag", "line1<br>line2", "line1\nline2"},
		{"br self-close", "a<br/>b", "a\nb"},
		{"br space self-close", "a<br />b", "a\nb"},
		{"p tag", "<p>hello</p><p>world</p>", "hello\nworld"},
		{"html entities", "a &amp; b &lt;c&gt;", "a & b <c>"},
		{"trim space", "  <p>hello</p>  ", "hello"},
		{"mixed", `<p>@user hello<br>world &amp; more</p>`, "@user hello\nworld & more"},
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("%s: stripHTML(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
