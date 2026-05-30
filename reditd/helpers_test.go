package main

// helpers_test.go — focused tests for the URL-classification and
// filename/MIME helpers that handle Reddit attachment metadata.
// None of these were covered by the existing test files.

import "testing"

func TestIsRedditImageURL_ImageHint(t *testing.T) {
	if !isRedditImageURL("https://example.com/whatever", "image") {
		t.Error("hint=image should be detected as image URL")
	}
}

func TestIsRedditImageURL_IReddIt(t *testing.T) {
	if !isRedditImageURL("https://i.redd.it/abc123.jpg", "") {
		t.Error("i.redd.it host should be detected")
	}
}

func TestIsRedditImageURL_ExtensionMatch(t *testing.T) {
	cases := []string{
		"https://example.com/photo.jpg",
		"https://example.com/photo.jpeg",
		"https://example.com/photo.png",
		"https://example.com/photo.gif",
		"https://example.com/photo.webp",
		"https://example.com/photo.png?w=600",
	}
	for _, u := range cases {
		if !isRedditImageURL(u, "") {
			t.Errorf("should detect image URL: %s", u)
		}
	}
}

func TestIsRedditImageURL_NonImage(t *testing.T) {
	cases := []string{
		"https://example.com/page",
		"https://reddit.com/r/golang",
		"https://v.redd.it/abc/DASH_720.mp4",
	}
	for _, u := range cases {
		if isRedditImageURL(u, "") {
			t.Errorf("should not detect as image URL: %s", u)
		}
	}
}

func TestMimeFromExt(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://i.redd.it/photo.png", "image/png"},
		{"https://i.redd.it/photo.gif", "image/gif"},
		{"https://i.redd.it/photo.webp", "image/webp"},
		{"https://i.redd.it/photo.jpg", "image/jpeg"},
		{"https://i.redd.it/photo.jpeg", "image/jpeg"},
		{"https://i.redd.it/photo", "image/jpeg"}, // fallback
		{"https://i.redd.it/photo.PNG", "image/png"},
	}
	for _, tc := range cases {
		got := mimeFromExt(tc.url)
		if got != tc.want {
			t.Errorf("mimeFromExt(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestFilenameFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://i.redd.it/abc123.jpg", "abc123.jpg"},
		{"https://i.redd.it/photo.png?source=fallback", "photo.png"},
		{"https://example.com/", "image.jpg"},  // base is "/"
		{"https://example.com/.", "image.jpg"}, // base is "."
		{"https://i.redd.it/a/b/c/image.webp", "image.webp"},
	}
	for _, tc := range cases {
		got := filenameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("filenameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestExtFromRedditMime(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/jpeg", ".jpg"},
		{"image/unknown", ".jpg"}, // fallback
		{"", ".jpg"},              // fallback
	}
	for _, tc := range cases {
		got := extFromRedditMime(tc.mime)
		if got != tc.want {
			t.Errorf("extFromRedditMime(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}
