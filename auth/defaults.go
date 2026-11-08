package auth

import (
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// defaults.go is the release's access policy, the ONLY place it is written.
// The evaluator reads these rows under the local rows in `acl`: for one call,
// a local row that matches decides, and these decide only when none does. An
// operator overrides a default with a local row and gets it back by revoking
// that row. The docs name this file and never copy it.

// memberVerbs are what every folder may do: answer the chats it is routed to.
// Scope ** because the route, not the scope, bounds where a message goes.
var memberVerbs = []string{
	"reply", "send", "send_file", "send_voice", "post", "forward", "quote",
	"repost", "like", "dislike", "edit", "delete", "disengage",
}

// skipApproval: the calls a world's agent makes without asking, one row per
// tool, and why a person need not see the call. A write that every turn
// mounts (invite_create, pin_message, reset_session) is NOT here: it waits.
var skipApproval = []struct{ tool, why string }{
	{"reply", "answers the chat the turn is in"},
	{"send", "a message to a chat the folder's routes already reach"},
	{"send_file", "send, with a file"},
	{"send_voice", "send, as speech"},
	{"post", "a post on an account the folder is routed to"},
	{"forward", "passes a message to a chat the folder already reaches"},
	{"quote", "a reply that quotes a message"},
	{"repost", "a repost on an account the folder is routed to"},
	{"like", "a reaction; it changes no content"},
	{"dislike", "a reaction; it changes no content"},
	{"edit", "the platforms let a bot edit only its own messages"},
	{"delete", "removes a message inside the platform's own bounds"},
	{"disengage", "stops the agent answering; it acts on nothing else"},
	{"inspect_routing", "reads the folder's routes"},
	{"inspect_tasks", "reads the folder's scheduled tasks"},
	{"inspect_session", "reads the turn's own session"},
	{"inspect_identity", "reads who is speaking"},
	{"inspect_messages", "reads the chat's messages"},
	{"find_messages", "searches the chat's messages"},
	{"get_thread", "reads one thread"},
	{"fetch_history", "reads the platform's history of the chat"},
	{"get_work", "reads the folder's work notes"},
	{"set_work", "writes the folder's own work notes, its memory checkpoint"},
}

// defaultRows is the default layer. Every row is on a role or a service, so
// it reaches exactly the principals the operator puts in that role, and a
// folder taken out of a role loses its rows.
var defaultRows = func() []core.ACLRow {
	rows := []core.ACLRow{
		// The delegation root: an operator holds everything, with the option.
		{Principal: store.RoleOperator, Action: "*", Scope: "**",
			Effect: store.EffectAllow, GrantOption: true},
		// dashd reads and writes chat settings on every folder.
		{Principal: "service:dashd", Action: "chats:get", Scope: "**", Effect: store.EffectAllow},
		{Principal: "service:dashd", Action: "chats:update", Scope: "**", Effect: store.EffectAllow},
		// A world holds every call for a person to see before it runs.
		{Principal: store.RoleWorld, Action: HoldPrefix + "*", Scope: "**",
			Effect: store.EffectAllow},
	}
	for _, v := range memberVerbs {
		row := core.ACLRow{Principal: store.RoleMember,
			Action: "mcp:" + v, Scope: "**", Effect: store.EffectAllow}
		if v == "send" {
			row.Params = "jid"
		}
		rows = append(rows, row)
	}
	for _, e := range skipApproval {
		rows = append(rows, core.ACLRow{Principal: store.RoleMember,
			Action: HoldPrefix + e.tool, Scope: "**", Effect: store.EffectDeny})
	}
	return rows
}()

// SkipApprovalTools returns the tools that skip approval by default.
func SkipApprovalTools() []string {
	names := make([]string, 0, len(skipApproval))
	for _, entry := range skipApproval {
		names = append(names, entry.tool)
	}
	return names
}
