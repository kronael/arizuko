package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
)

func cmdChatThreadReplies(args []string) {
	if len(args) < 3 || len(args) > 4 {
		die("usage: arizuko chat <instance> thread-replies <jid> [on|off|default]")
	}
	mustInstanceDir(args[0])
	db := mustOpenRoutd()
	defer func() { _ = db.Close() }()
	user := os.Getenv("USER")
	s := store.NewOver(db.Obj()).AsCLI(user)
	if err := runChatThreadReplies(s, user, args[2:], os.Stdout); err != nil {
		die("Failed: %v", err)
	}
}

func runChatThreadReplies(s *store.Store, user string, args []string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 || args[0] == "" {
		return fmt.Errorf("expected <jid> [on|off|default]")
	}
	jid := args[0]
	if len(args) == 2 {
		var pref *bool
		switch args[1] {
		case "on":
			pref = new(true)
		case "off":
			pref = new(false)
		case "default":
		default:
			return fmt.Errorf("expected on|off|default")
		}
		var ws store.WriteSet
		if err := s.SetChatThreadRepliesInto(&ws, jid, pref); err != nil {
			return err
		}
		_, record := audit.WriteFor(audit.Event{
			Category: audit.CategoryMutation, Action: "chats:update", Actor: "cli:" + user,
			ActorSub: user, Surface: audit.SurfaceCLI, Resource: "chats", Outcome: audit.OutcomeOK,
			ParamsSummary: map[string]any{"jid": jid, "thread_replies": pref},
		})
		if err := s.Apply(context.Background(), &ws, record); err != nil {
			return err
		}
	}
	on, err := s.ChatThreadReplies(jid)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s thread replies: %t\n", jid, on)
	return err
}
