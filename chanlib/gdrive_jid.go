package chanlib

import "strings"

// GDriveJID is a parsed Google Drive JID (specs/6/45-gdrive-comments-channel.md):
//
//	gdrive:<fileId>                 one file (observe only)
//	gdrive:<fileId>/c/<commentId>   one comment thread
//
// Each thread has its own JID, so the thread address lives here and nowhere
// else. Outbound replies are addressed from the JID alone.
type GDriveJID struct {
	FileID    string
	CommentID string
}

// ParseGDriveJID returns the parsed parts and ok=true on a well-formed JID.
// Each segment is non-empty and made of [A-Za-z0-9_-].
func ParseGDriveJID(jid string) (GDriveJID, bool) {
	rest, found := strings.CutPrefix(jid, "gdrive:")
	if !found || rest == "" {
		return GDriveJID{}, false
	}
	seg := strings.Split(rest, "/")
	for _, s := range seg {
		if !isGDriveSegment(s) {
			return GDriveJID{}, false
		}
	}
	switch len(seg) {
	case 1:
		return GDriveJID{FileID: seg[0]}, true
	case 3:
		if seg[1] != "c" {
			return GDriveJID{}, false
		}
		return GDriveJID{FileID: seg[0], CommentID: seg[2]}, true
	}
	return GDriveJID{}, false
}

func isGDriveSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// FormatGDriveJID is the inverse of ParseGDriveJID. Empty commentID → file level.
func FormatGDriveJID(fileID, commentID string) string {
	jid := "gdrive:" + fileID
	if commentID == "" {
		return jid
	}
	return jid + "/c/" + commentID
}

func (j GDriveJID) String() string {
	return FormatGDriveJID(j.FileID, j.CommentID)
}

// Topic is the InboundMsg.Topic for this JID: `c/<commentId>`, empty at file
// level. sessions is keyed (folder, topic), so this field is what gives two
// threads in one folder two sessions. It is a session key, never an address:
// it carries the comment id and no other segment.
func (j GDriveJID) Topic() string {
	if j.CommentID == "" {
		return ""
	}
	return "c/" + j.CommentID
}
