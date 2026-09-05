// Package v1 is routd's published contract: the wire types of the ingress
// /v1/messages endpoint, the daemon-facing /v1/outbound, the route/token CRUD
// and the turn result submit_turn carries. Imports only types/ (spec 5/E §
// api/v1 surface) so a caller never drags in core.
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
	Source         string       `json:"source"`       // adapter channel name (CHANNEL_NAME); persisted for multi-account reply routing
	LinkContext    string       `json:"link_context"` // route-token context snapshotted at ingest by webd (spec 5/W)
	Attachments    []Attachment `json:"attachments"`
	Attachment     string       `json:"attachment"` // whapd flat-attachment compatibility
	AttachmentMime string       `json:"attachment_mime"`
	AttachmentName string       `json:"attachment_name"`
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

// OK is the {ok:true} response of a write that returns nothing else.
type OK struct {
	OK bool `json:"ok"`
}

// TurnResult is the submit_turn payload. The cost breakdown is reported by
// the agent runtime; routd persists it into cost_log (runed never writes
// cost).
type TurnResult struct {
	TurnID    string               `json:"turn_id"`
	SessionID string               `json:"session_id"`
	Status    string               `json:"status"` // success|error
	Result    string               `json:"result"`
	Error     string               `json:"error"`
	CallerSub string               `json:"caller_sub"`
	Models    map[string]ModelCost `json:"models"`
	// TimedOut marks the graceful query-timeout summary. routd logs a WARN;
	// the outcome stays OK (no breaker trip).
	TimedOut bool `json:"timed_out"`
}

// ModelCost is one per-model cost row in TurnResult.Models.
type ModelCost struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	CostCents int `json:"cost_cents"`
}

// OutboundRequest is POST /v1/outbound (timed/onbod → routd → adapter).
// routd appends the messages row it delivers.
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

// RouteTokenRequest is POST /v1/route_tokens/{chat,hook}. The response and list
// shapes are NOT typed here: both faces share routeTokensHandler, which emits the
// unified {token,jid,url} / {tokens:[…]} shapes (route_tokens_resource.go, spec
// 5/16). This struct is the request contract.
type RouteTokenRequest struct {
	OwnerFolder  string `json:"owner_folder"`
	TargetFolder string `json:"target_folder"`
	SourceLabel  string `json:"source_label"` // hook only
	JIDSuffix    string `json:"jid_suffix"`
	Context      string `json:"context"` // optional per-link processing instructions (spec 5/W)
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

// UnroutedChat is one row of GET /v1/routing/unrouted: a chat whose latest
// inbound message no route, direct address or engagement window claims (spec
// 6/35). Held, not delivered — the operator claims it by writing a route.
type UnroutedChat struct {
	ChatJID    string `json:"chat_jid"`
	Platform   string `json:"platform"`
	ChatName   string `json:"chat_name,omitempty"`
	Count      int    `json:"count"`
	FirstAt    string `json:"first_at"`
	LastAt     string `json:"last_at"`
	LastSender string `json:"last_sender"`
}

// UnroutedChatsResponse is GET /v1/routing/unrouted.
type UnroutedChatsResponse struct {
	Chats []UnroutedChat `json:"chats"`
}

// EngagementResponse is GET /v1/engagement?jid&topic: the engaged folder and
// deadline (both "" when the window is not live) plus the (jid,topic) thread
// anchor last_reply_id, which outlives the window and is reported regardless.
type EngagementResponse struct {
	Folder       string `json:"folder"`
	EngagedUntil string `json:"engaged_until"`
	LastReplyID  string `json:"last_reply_id"`
}

// EngagedChat is one live window in the GET /v1/engagement list.
type EngagedChat struct {
	JID          string `json:"jid"`
	Topic        string `json:"topic"`
	Folder       string `json:"folder"`
	EngagedUntil string `json:"engaged_until"`
}

// EngagementListResponse is GET /v1/engagement with no jid: every live window
// the caller may see, newest deadline first. Contained to the caller's subtree
// unless the token carries an empty (root/service) folder claim.
type EngagementListResponse struct {
	Engaged []EngagedChat `json:"engaged"`
}

// EngagementRequest is POST /v1/engagement (engage/disengage). TTLSeconds<=0
// clears the engagement (disengage).
type EngagementRequest struct {
	JID        string `json:"jid"`
	Topic      string `json:"topic"`
	Folder     string `json:"folder"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// SessionResponse is GET /v1/topic_sessions?folder&topic: the resume session id
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

// CostResponse is GET /v1/cost?turn_id=: the turn's recorded spend summed
// across cost_log models. 404 when the turn has no rows yet — distinct from
// a genuine zero-cost turn.
type CostResponse struct {
	TurnID       string `json:"turn_id"`
	Folder       string `json:"folder"`
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
