package main

import "testing"

// TestDiscordMimeFor verifies the lookup table covers each rich-media
// surface Discord renders inline (image bubble, video player, audio
// player) and falls back to "" for unknown extensions so Discord
// defaults to application/octet-stream.
func TestDiscordMimeFor(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"a.png", "image/png"},
		{"b.JPG", "image/jpeg"},
		{"c.jpeg", "image/jpeg"},
		{"d.gif", "image/gif"},
		{"e.webp", "image/webp"},
		{"f.mp4", "video/mp4"},
		{"g.mov", "video/quicktime"},
		{"h.webm", "video/webm"},
		{"i.mp3", "audio/mpeg"},
		{"j.ogg", "audio/ogg"},
		{"k.opus", "audio/ogg"},
		{"l.m4a", "audio/mp4"},
		{"m.flac", "audio/flac"},
		{"n.pdf", "application/pdf"},
		{"o.bin", ""},
		{"noext", ""},
	}
	for _, tc := range cases {
		got := discordMimeFor(tc.name)
		if got != tc.want {
			t.Errorf("discordMimeFor(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
