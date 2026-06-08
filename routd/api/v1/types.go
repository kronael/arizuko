// Package v1 is routd's published contract: the wire types + a thin HTTP
// client for the /v1/turns/{turn_id}/* callback surface and the ingress
// /v1/messages endpoint. Imports only types/ (spec 5/U § Per-service
// api/v1) so runed can call back without dragging in core.
//
// The /v1/turns/{turn_id}/* bodies are PINNED, identical to the peer
// rendering in specs/5/P-runed.md § The agent's callback into routd and
// specs/5/E-routd.md § Turn / conversation commands.
package v1

// Message is the inbound body of POST /v1/messages (adapter → routd).
// routd appends exactly one messages row from it. Timestamp is unix
// seconds; 0/absent → now.
type Message struct {
	ID             string       `json:"id"` // optional; routd mints <adapter>-<rand> if empty
	ChatJID        string       `json:"chat_jid"`
	Sender         string       `json:"sender"`
	SenderName     string       `json:"sender_name"`
	Content        string       `json:"content"`
	Timestamp      int64        `json:"timestamp"`
	ReplyTo        string       `json:"reply_to"`
	ReplyToText    string       `json:"reply_to_text"`
	ReplyToSender  string       `json:"reply_to_sender"`
	ForwardedFrom  string       `json:"forwarded_from"` // delegation return-address (spec 5/E § delegation)
	Topic          string       `json:"topic"`
	Verb           string       `json:"verb"`     // default message; like|dislike|edit|delete|...
	Reaction       string       `json:"reaction"` // emoji for verb=like
	IsGroup        bool         `json:"is_group"`
	ChatName       string       `json:"chat_name"`
	Source         string       `json:"source"` // adapter channel name (CHANNEL_NAME); persisted for multi-account reply routing
	Attachments    []Attachment `json:"attachments"`
	Attachment     string       `json:"attachment"`      // whapd flat-attachment compatibility
	AttachmentMime string       `json:"attachment_mime"` //
	AttachmentName string       `json:"attachment_name"` //
}

// Attachment is one inbound media item.
type Attachment struct {
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Data     string `json:"data"`
}

// MessageAck is the 200 of POST /v1/messages; id echoes the stored row.
type MessageAck struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

// ReplyRequest is POST /v1/turns/{turn_id}/reply and /send (send ignores
// ReplyToID).
type ReplyRequest struct {
	JID       string `json:"jid"`
	Text      string `json:"text"`
	ReplyToID string `json:"reply_to_id"`
}

// DocumentRequest is POST /v1/turns/{turn_id}/document. The file at Path
// lives on the shared group volume both routd and the adapter mount.
type DocumentRequest struct {
	JID       string `json:"jid"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Caption   string `json:"caption"`
	ReplyToID string `json:"reply_to_id"`
}

// SendResult is the response of reply/send (carries platform_id) and
// document (no platform_id until delivered).
type SendResult struct {
	MessageID  string `json:"message_id"`
	PlatformID string `json:"platform_id"`
	Status     string `json:"status"` // sent|pending
}

// ReactionRequest is POST /v1/turns/{turn_id}/like.
type ReactionRequest struct {
	JID        string `json:"jid"`
	PlatformID string `json:"platform_id"`
	Reaction   string `json:"reaction"`
}

// EditRequest is POST /v1/turns/{turn_id}/edit.
type EditRequest struct {
	JID        string `json:"jid"`
	PlatformID string `json:"platform_id"`
	Content    string `json:"content"`
}

// TargetRequest is POST /v1/turns/{turn_id}/{delete,pin,unpin}. all is
// honored only by /unpin.
type TargetRequest struct {
	JID        string `json:"jid"`
	PlatformID string `json:"platform_id"`
	All        bool   `json:"all"`
}

// OK is the {ok:true} response of like/edit/delete/pin/unpin.
type OK struct {
	OK bool `json:"ok"`
}

// HistoryMessage is one row of GET /v1/turns/{turn_id}/history / thread.
type HistoryMessage struct {
	ID         string `json:"id"`
	Sender     string `json:"sender"`
	Content    string `json:"content"`
	Timestamp  string `json:"timestamp"`
	ReplyToID  string `json:"reply_to_id"`
	IsFromMe   bool   `json:"is_from_me"`
	IsBotMsg   bool   `json:"is_bot_message"`
	PlatformID string `json:"platform_id"`
}

// HistoryResponse is GET /v1/turns/{turn_id}/history.
type HistoryResponse struct {
	Source   string           `json:"source"` // cache|platform|cache-only
	Messages []HistoryMessage `json:"messages"`
	Cap      int              `json:"cap"`
}

// ThreadResponse is GET /v1/turns/{turn_id}/thread.
type ThreadResponse struct {
	Messages []HistoryMessage `json:"messages"`
}

// TurnResult is submit_turn / POST /v1/turns/{turn_id}/result. The cost
// breakdown is reported by runed; routd persists it into cost_log (runed
// never writes cost).
type TurnResult struct {
	TurnID    string               `json:"turn_id"`
	SessionID string               `json:"session_id"`
	Status    string               `json:"status"` // success|error
	Result    string               `json:"result"`
	Error     string               `json:"error"`
	CallerSub string               `json:"caller_sub"`
	Models    map[string]ModelCost `json:"models"`
}

// ModelCost is one per-model cost row in TurnResult.Models.
type ModelCost struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	CostCents int `json:"cost_cents"`
}

// TurnResultAck is the response of /v1/turns/{turn_id}/result; recorded
// false = duplicate (folder,turn_id), ignored.
type TurnResultAck struct {
	Recorded bool `json:"recorded"`
}

// OutboundRequest is POST /v1/outbound (timed/onbod → routd → adapter).
// Does NOT append a messages row.
type OutboundRequest struct {
	JID     string `json:"jid"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
}

// Route is one /v1/routes row (mirrors core.Route on the wire).
type Route struct {
	ID                    int64  `json:"id"`
	Seq                   int    `json:"seq"`
	Match                 string `json:"match"`
	Target                string `json:"target"`
	ObserveWindowMessages int    `json:"observe_window_messages,omitempty"`
	ObserveWindowChars    int    `json:"observe_window_chars,omitempty"`
}

// WebRoute is one /v1/web_routes row.
type WebRoute struct {
	PathPrefix string `json:"path_prefix"`
	Access     string `json:"access"` // public|auth|deny|redirect
	RedirectTo string `json:"redirect_to"`
	Folder     string `json:"folder"`
	CreatedAt  string `json:"created_at"`
}

// RouteTokenRequest is POST /v1/route_tokens/{chat,hook}.
type RouteTokenRequest struct {
	OwnerFolder  string `json:"owner_folder"`
	TargetFolder string `json:"target_folder"`
	SourceLabel  string `json:"source_label"` // hook only
	JIDSuffix    string `json:"jid_suffix"`
}

// RouteTokenResponse is the 201 of route-token issue (raw token once).
type RouteTokenResponse struct {
	Token       string `json:"token"`
	URL         string `json:"url"`
	JID         string `json:"jid"`
	OwnerFolder string `json:"owner_folder"`
	CreatedAt   string `json:"created_at"`
}

// RouteTokenRow is one GET /v1/route_tokens entry (never the raw token).
type RouteTokenRow struct {
	JID         string `json:"jid"`
	OwnerFolder string `json:"owner_folder"`
	CreatedAt   string `json:"created_at"`
}

// ResolveRequest is POST /v1/route_tokens/resolve (webd → routd).
type ResolveRequest struct {
	Token string `json:"token"`
}

// ResolveResponse is the 200 of /v1/route_tokens/resolve.
type ResolveResponse struct {
	JID         string `json:"jid"`
	OwnerFolder string `json:"owner_folder"`
}

// MessageRow is one full message row of the agent read surface
// (/v1/messages/{inspect,thread}). It carries the columns runed needs to
// reconstruct a core.Message for the agent's formatter — richer than
// HistoryMessage (the turn-scoped history body).
type MessageRow struct {
	ID            string `json:"id"`
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name"`
	Content       string `json:"content"`
	Timestamp     string `json:"timestamp"`
	IsFromMe      bool   `json:"is_from_me"`
	IsBotMsg      bool   `json:"is_bot_message"`
	ReplyToID     string `json:"reply_to_id"`
	Topic         string `json:"topic"`
	RoutedTo      string `json:"routed_to"`
	Verb          string `json:"verb"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	PlatformID    string `json:"platform_id"`
	ChatName      string `json:"chat_name"`
	ForwardedFrom string `json:"forwarded_from"`
}

// MessagesResponse is GET /v1/messages/{inspect,thread}: full rows + count.
type MessagesResponse struct {
	Messages []MessageRow `json:"messages"`
	Count    int          `json:"count"`
}

// FoundMessage is one find_messages hit (FTS5 snippet + BM25 rank).
type FoundMessage struct {
	ChatJID      string  `json:"chat_jid"`
	Sender       string  `json:"sender"`
	Timestamp    string  `json:"timestamp"`
	IsFromMe     bool    `json:"is_from_me"`
	IsBotMessage bool    `json:"is_bot_message"`
	Content      string  `json:"content"`
	Rank         float64 `json:"rank"`
}

// FindResponse is GET /v1/messages/find.
type FindResponse struct {
	Messages []FoundMessage `json:"messages"`
	Count    int            `json:"count"`
}

// RoutingResolveResponse is GET /v1/routing/resolve?jid[&folder]. Folder is
// the default route target; Routed is set only when folder= is supplied
// (DefaultFolderForJID vs JIDRoutedToFolder).
type RoutingResolveResponse struct {
	Folder string `json:"folder"`
	Routed bool   `json:"routed"`
}

// ErroredChat is one row of GET /v1/routing/errored.
type ErroredChat struct {
	ChatJID  string `json:"chat_jid"`
	Count    int    `json:"count"`
	LastAt   string `json:"last_at"`
	RoutedTo string `json:"routed_to"`
}

// ErroredChatsResponse is GET /v1/routing/errored.
type ErroredChatsResponse struct {
	Chats []ErroredChat `json:"chats"`
}

// EngagementResponse is GET /v1/engagement?jid&topic: the engaged folder
// ("" when none) and the (jid,topic) thread anchor last_reply_id.
type EngagementResponse struct {
	Folder      string `json:"folder"`
	LastReplyID string `json:"last_reply_id"`
}

// EngagementRequest is POST /v1/engagement (engage/disengage). TTLSeconds<=0
// clears the engagement (disengage).
type EngagementRequest struct {
	JID        string `json:"jid"`
	Topic      string `json:"topic"`
	Folder     string `json:"folder"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// SessionResponse is GET /v1/sessions?folder&topic: the resume session id
// for (folder, topic), "" when none.
type SessionResponse struct {
	SessionID string `json:"session_id"`
}

// CostRequest is POST /v1/cost: one external-LLM cost_log row.
type CostRequest struct {
	Folder       string `json:"folder"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CostCents    int    `json:"cost_cents"`
}

// Err is the uniform JSON error envelope.
type Err struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Status values shared with the message store.
const (
	StatusSent    = "sent"
	StatusPending = "pending"
	StatusFailed  = "failed"
)
