package main

import (
	"testing"
)

func TestMapThread(t *testing.T) {
	thread := driveComment{driveReply: driveReply{ID: "c1", CreatedTime: "2026-01-01T00:00:00Z", Content: "question", Author: driveAuthor{DisplayName: "Ada"}}}
	thread.Quoted.Value = "quoted text"
	thread.Replies = []driveReply{
		{ID: "r1", Content: "answer", Author: driveAuthor{Me: true}},
		{ID: "r2", Content: "follow up", Author: driveAuthor{DisplayName: "Bob"}, Mentioned: []string{"bot@example.com"}},
		{ID: "r3", Action: "resolve", Author: driveAuthor{DisplayName: "Bob"}},
		{ID: "r4", Deleted: true},
	}
	msgs := mapThread("file", "Document", "gdrive", "bot@example.com", thread)
	if len(msgs) != 3 {
		t.Fatalf("messages = %#v", msgs)
	}
	if msgs[0].ID != "gd-cfile.c1" || msgs[0].Content != "> quoted text\n\nquestion" || msgs[0].Topic != "c/c1" || msgs[0].ChatJID != "gdrive:file/c/c1" || msgs[0].Timestamp != 1767225600 || msgs[0].Sender != "gdrive:Ada" || !msgs[0].IsGroup || msgs[0].ChatName != "Document" || msgs[0].Source != "gdrive" {
		t.Errorf("head = %#v", msgs[0])
	}
	if msgs[1].ID != "gd-rfile.c1.r2" || msgs[1].ReplyTo != "gd-rfile.c1.r1" || msgs[1].Verb != "mention" {
		t.Errorf("reply = %#v", msgs[1])
	}
	if msgs[2].Verb != "resolve" || msgs[2].ReplyTo != "gd-rfile.c1.r2" {
		t.Errorf("resolution = %#v", msgs[2])
	}
}
