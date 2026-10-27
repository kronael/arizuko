package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

type ChatSettingsRequest struct {
	ThreadReplies *bool `json:"thread_replies,omitempty"`
}

type ChatSettingsResponse struct {
	JID           string `json:"jid"`
	ThreadReplies bool   `json:"thread_replies"`
}

var ChatsMCPNames = map[resreg.Action]string{
	resreg.ActionGet: "get_chat", resreg.ActionUpdate: "set_chat",
}

func init() {
	resreg.Register(resreg.Resource{
		Name: ChatsName, Table: "chats", RowType: reflect.TypeFor[ChatSettingsRequest](),
		Endpoints: []resreg.Endpoint{
			{Verb: "GET", Path: "/v1/chats/{jid}", Action: resreg.ActionGet},
			{Verb: "PUT", Path: "/v1/chats/{jid}", Action: resreg.ActionUpdate},
		},
		Responses: map[resreg.Action]reflect.Type{
			resreg.ActionGet:    reflect.TypeFor[ChatSettingsResponse](),
			resreg.ActionUpdate: reflect.TypeFor[ChatSettingsResponse](),
		},
		MCPNames: ChatsMCPNames,
		MCPDoc: map[resreg.Action]string{
			resreg.ActionGet:    "Read the effective thread reply preference for a chat.",
			resreg.ActionUpdate: "Set the thread reply preference for a chat. Omit thread_replies to reset its default.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			resreg.ActionGet:    {{Name: "jid", Type: "string", Required: true}},
			resreg.ActionUpdate: {{Name: "jid", Type: "string", Required: true}, {Name: "thread_replies", Type: "bool", Description: "Omit to reset the chat default."}},
		},
	})
}
