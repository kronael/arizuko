package chanlib

import (
	"strings"
	"testing"
)

func TestParseGDriveJID(t *testing.T) {
	cases := []struct {
		jid  string
		want GDriveJID
		ok   bool
	}{
		{"gdrive:1AbC_dEf-9", GDriveJID{FileID: "1AbC_dEf-9"}, true},
		{"gdrive:1AbC/c/AAAB-x_9", GDriveJID{FileID: "1AbC", CommentID: "AAAB-x_9"}, true},
		{"gdrive:", GDriveJID{}, false},
		{"gdrive:/c/x", GDriveJID{}, false},
		{"gdrive:f/", GDriveJID{}, false},
		{"gdrive:f/c", GDriveJID{}, false},
		{"gdrive:f/c/", GDriveJID{}, false},
		{"gdrive:f//c/x", GDriveJID{}, false},
		{"gdrive:f/c//x", GDriveJID{}, false},
		{"gdrive:f/x/y", GDriveJID{}, false},
		{"gdrive:f/C/y", GDriveJID{}, false},
		{"gdrive:f/c/y/z", GDriveJID{}, false},
		{"gdrive:f/c/y/", GDriveJID{}, false},
		{"gdrive:f g", GDriveJID{}, false},
		{"gdrive:f.g", GDriveJID{}, false},
		{"gdrive:f/c/y z", GDriveJID{}, false},
		{"gdrive:f%2Fc", GDriveJID{}, false},
		{"gdrive:f\n", GDriveJID{}, false},
		{"github:f/c/y", GDriveJID{}, false},
		{"f/c/y", GDriveJID{}, false},
		{"", GDriveJID{}, false},
	}
	for _, c := range cases {
		got, ok := ParseGDriveJID(c.jid)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseGDriveJID(%q) = %+v, %v; want %+v, %v", c.jid, got, ok, c.want, c.ok)
		}
		if ok && got.String() != c.jid {
			t.Errorf("round trip %q -> %q", c.jid, got.String())
		}
	}
}

func TestFormatGDriveJID(t *testing.T) {
	cases := []struct {
		fileID, commentID string
		want              string
	}{
		{"1AbC", "", "gdrive:1AbC"},
		{"1AbC", "AAAB", "gdrive:1AbC/c/AAAB"},
	}
	for _, c := range cases {
		if got := FormatGDriveJID(c.fileID, c.commentID); got != c.want {
			t.Errorf("FormatGDriveJID(%q,%q) = %q, want %q", c.fileID, c.commentID, got, c.want)
		}
	}
}

func TestGDriveJIDTopic(t *testing.T) {
	cases := map[string]string{
		"gdrive:1AbC":        "",
		"gdrive:1AbC/c/AAAB": "c/AAAB",
	}
	for jid, want := range cases {
		j, ok := ParseGDriveJID(jid)
		if !ok {
			t.Fatalf("parse %q", jid)
		}
		if got := j.Topic(); got != want {
			t.Errorf("Topic(%q) = %q, want %q", jid, got, want)
		}
	}
}

// A topic is a session key, never an address (BUGS L412/L421): it is empty or
// exactly `c/<commentId>`. A change that encodes the file id, a reply id or any
// further segment into it fails here.
func TestGDriveJIDTopicCarriesNoSecondAddressSegment(t *testing.T) {
	jids := []string{
		"gdrive:FILEID",
		"gdrive:FILEID/c/COMMENTID",
		"gdrive:a-b_c/c/d-e_f",
	}
	for _, jid := range jids {
		j, ok := ParseGDriveJID(jid)
		if !ok {
			t.Fatalf("parse %q", jid)
		}
		topic := j.Topic()
		if topic == "" {
			if j.CommentID != "" {
				t.Errorf("Topic(%q) is empty for a thread", jid)
			}
			continue
		}
		if want := "c/" + j.CommentID; topic != want {
			t.Errorf("Topic(%q) = %q, want exactly %q", jid, topic, want)
		}
		if n := len(strings.Split(topic, "/")); n != 2 {
			t.Errorf("Topic(%q) = %q has %d segments, want 2", jid, topic, n)
		}
		if strings.Contains(topic, j.FileID) {
			t.Errorf("Topic(%q) = %q carries the file id", jid, topic)
		}
	}
}
