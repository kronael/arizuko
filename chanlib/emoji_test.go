package chanlib

import "testing"

func TestClassifyEmoji(t *testing.T) {
	cases := []struct {
		emoji, want string
	}{
		{"👍", "like"},
		{"❤️", "like"},
		{"🎉", "like"},
		{"⭐", "like"},
		{"👎", "dislike"},
		{"💩", "dislike"},
		{"😡", "dislike"},
		{"🤬", "dislike"},
		{"💔", "dislike"},
		{"🤮", "dislike"},
		{"😢", "dislike"},
		{"🦄", "like"},
		{"", "like"},
	}
	for _, c := range cases {
		if got := ClassifyEmoji(c.emoji); got != c.want {
			t.Errorf("ClassifyEmoji(%q) = %q, want %q", c.emoji, got, c.want)
		}
	}
}
