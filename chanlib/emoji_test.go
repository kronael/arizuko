package chanlib

import "testing"

// Slack names carry the same sentiment as their Unicode equivalents.
func TestClassifySlackEmoji(t *testing.T) {
	for _, emoji := range []string{
		"thumbsdown", "-1", "poop", "shit", "hankey", "rage",
		"face_with_symbols_on_mouth", "broken_heart", "face_vomiting", "cry",
	} {
		if got := ClassifyEmoji(emoji); got != "dislike" {
			t.Errorf("ClassifyEmoji(%q) = %q, want dislike", emoji, got)
		}
	}
}

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
