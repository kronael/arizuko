package expand

import "testing"

func TestExpand(t *testing.T) {
	vars := map[string]string{"jid": "github:a/b/pr/12", "thread": "", "sender": "dc-1", "secret:KEY": "{jid}"}
	got, err := Expand("{jid}|{thread}|{sender}|{secret:KEY}|{jid}", vars)
	if err != nil || got != "github:a/b/pr/12||dc-1|{jid}|github:a/b/pr/12" {
		t.Fatalf("Expand = %q, %v", got, err)
	}
	for _, input := range []string{"{missing}", "{jid", "jid}", "{{jid}}"} {
		if _, err := Expand(input, vars); err == nil {
			t.Errorf("Expand(%q) accepted invalid template", input)
		}
	}
}
