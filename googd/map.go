package main

import (
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

type driveAuthor struct {
	DisplayName string `json:"displayName"`
	Me          bool   `json:"me"`
}
type driveReply struct {
	ID           string      `json:"id"`
	CreatedTime  string      `json:"createdTime"`
	ModifiedTime string      `json:"modifiedTime"`
	Content      string      `json:"content"`
	Action       string      `json:"action"`
	Deleted      bool        `json:"deleted"`
	Author       driveAuthor `json:"author"`
	Mentioned    []string    `json:"mentionedEmailAddresses"`
	Assignee     string      `json:"assigneeEmailAddress"`
}
type driveComment struct {
	driveReply
	Quoted struct {
		Value string `json:"value"`
	} `json:"quotedFileContent"`
	Replies []driveReply `json:"replies"`
}

func mapThread(file, name, source, account string, comment driveComment) []chanlib.InboundMsg {
	jid := chanlib.GDriveJID{FileID: file, CommentID: comment.ID}
	headID := "gd-c" + file + "." + comment.ID
	var messages []chanlib.InboundMsg
	appendPost := func(post driveReply, id, replyTo, content string) {
		if post.Deleted || post.Author.Me {
			return
		}
		verb := "message"
		if account != "" && (strings.EqualFold(post.Assignee, account) || containsEmail(post.Mentioned, account)) {
			verb = "mention"
		}
		if post.Action == "resolve" {
			verb = "resolve"
		} else if post.Action == "reopen" && post.Content == "" {
			verb = "reopen"
		}
		if content == "" && verb == "resolve" {
			content = "Comment resolved"
		} else if content == "" && verb == "reopen" {
			content = "Comment reopened"
		}
		timestamp, _ := time.Parse(time.RFC3339Nano, post.CreatedTime)
		messages = append(messages, chanlib.InboundMsg{ID: id, ChatJID: jid.String(), Topic: jid.Topic(), Sender: "gdrive:" + post.Author.DisplayName, SenderName: post.Author.DisplayName, Content: content, Timestamp: timestamp.Unix(), ReplyTo: replyTo, Verb: verb, IsGroup: true, ChatName: name, Source: source})
	}
	content := comment.Content
	if comment.Quoted.Value != "" {
		content = "> " + strings.ReplaceAll(comment.Quoted.Value, "\n", "\n> ") + "\n\n" + content
	}
	appendPost(comment.driveReply, headID, "", content)
	preceding := headID
	for _, reply := range comment.Replies {
		id := "gd-r" + file + "." + comment.ID + "." + reply.ID
		appendPost(reply, id, preceding, reply.Content)
		preceding = id
	}
	return messages
}
func containsEmail(emails []string, account string) bool {
	for _, email := range emails {
		if strings.EqualFold(email, account) {
			return true
		}
	}
	return false
}
