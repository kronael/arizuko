// Package v1 is routd's published contract: the wire types of the ingress
// /v1/messages endpoint, the daemon-facing /v1/outbound, the route/token CRUD
// and the turn result submit_turn carries. Imports only types/ (spec 5/E-routd §
// api/v1 surface) so a caller never drags in core.
package v1

// Message is the inbound body of POST /v1/messages (adapter → routd).
// routd appends exactly one messages row from it. Timestamp is unix
// seconds; 0/absent → now.
type Message struct {
	ID            string `json:"id"` // optional; routd mints <adapter>-<rand> if empty
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name"`
	Content       string `json:"content"`
	Timestamp     int64  `json:"timestamp"`
	ReplyTo       string `json:"reply_to"`
	ReplyToText   string `json:"reply_to_text"`
	ReplyToSender string `json:"reply_to_sender"`
	ForwardedFrom string `json:"forwarded_from"` // delegation return-address (spec 5/E-routd § delegation)
	Topic         string `json:"topic"`
	Verb          string `json:"verb"`     // default message; like|dislike|edit|delete|...
	Reaction      string `json:"reaction"` // emoji for verb=like
	// IsGroup is the chat's classification: true for a group or channel, false for
	// a DM, NIL for a caller that has none to give. The third state is the point,
	// because routd WRITES this onto `chats.is_group`: only the adapter that
	// OBSERVES the chat classifies it, while timed's cron fire and an eval
	// injection merely post into a chat someone else observes. A bare bool spells
	// their silence "DM", which unflagged every group chat a scheduled task fires
	// into and answered a live group in a direct chat's reply shape (BUGS L306).
	IsGroup        *bool        `json:"is_group,omitempty"`
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

// TurnResult is the submit_turn payload. The cost breakdown is the agent
// runtime's own report, which routd logs and never stores — the model gateway
// is the ledger (spec 6/39).
type TurnResult struct {
	TurnID    string               `json:"turn_id"`
	SessionID string               `json:"session_id"`
	Status    string               `json:"status"` // success|error
	Error     string               `json:"error"`
	Models    map[string]ModelCost `json:"models"`
	// TimedOut reports harness time or turn-limit exhaustion.
	TimedOut bool `json:"timed_out"`
}

// ModelCost is one model's self-reported usage in TurnResult.Models.
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
	PlatformID    string `json:"platform_id"`
	ChatName      string `json:"chat_name"`
	ForwardedFrom string `json:"forwarded_from"`
}

// A message row carries NO delivery status. `sent` is the absence of a delivery
// item rather than a value on the message, so the state a caller can act on is
// the dead-letter resource — which is the set of rows that are not sent, and the
// only set an operator does anything with (spec 6/37 §"How `messages` leaves").

// MessagesResponse is GET /v1/messages/{inspect,thread}: full rows + count.
type MessagesResponse struct {
	Messages []MessageRow `json:"messages"`
	Count    int          `json:"count"`
}

// FoundMessage is one find_messages hit: the «»-marked snippet and its BM25
// rank, where lower is the better match.
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
// 6/35-unrouted-traffic). Held, not delivered — the operator claims it by writing a route.
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

// TurnOutcomes counts the turns dispatched in the window by the state they
// reached. `Done` and `Expired` are the terminal pair; a window whose Expired is
// climbing is a fleet whose turns stop before they answer.
//
// `Other` catches a state none of the four names. It exists so the four plus it
// always sum to the turns in the window: a state added later would otherwise
// leave this surface under-reporting, which is the failure the surface is for.
type TurnOutcomes struct {
	Done         int `json:"done"`
	Expired      int `json:"expired"`
	Running      int `json:"running"`
	PendingRetry int `json:"pending_retry"`
	Other        int `json:"other"`
}

// The verdicts a WaitingChat carries: what the loop does with this chat's newest
// human message TODAY. They are the outcomes of routd's own resolve, so the
// surface cannot disagree with the loop it describes.
const (
	// VerdictRoute — a turn is due and the folder is named. Waiting here is a
	// fault: someone wrote, the loop would answer, and no reply came.
	VerdictRoute = "route"
	// VerdictObserve — an #observe route consumes the message. No turn runs, by
	// the operator's own configuration.
	VerdictObserve = "observe"
	// VerdictHeld — no route, direct address or engagement window claims the
	// message. A `verb=mention` rule and a plain message land here: the chat is
	// deaf by configuration, not broken.
	VerdictHeld = "held"
)

// WaitingChat is one chat whose newest human message is newer than its newest
// agent reply — someone wrote and nothing answered.
//
// Verdict is what makes the row readable. "Waiting" alone cannot tell a stalled
// turn from a chat the operator configured not to answer, and reporting the two
// the same way is what made this question unanswerable without hand-written SQL.
type WaitingChat struct {
	ChatJID  string `json:"chat_jid"`
	Platform string `json:"platform"`
	ChatName string `json:"chat_name,omitempty"`
	// Folder is where the answer would come from; empty under VerdictHeld,
	// which is a chat no folder has claimed.
	Folder      string `json:"folder,omitempty"`
	Verdict     string `json:"verdict"`
	LastHumanAt string `json:"last_human_at"`
	// LastReplyAt is empty when the agent has never answered this chat at all —
	// a different fact from an answer that arrived before the question.
	LastReplyAt string `json:"last_reply_at,omitempty"`
	LastSender  string `json:"last_sender"`
	WaitingSec  int    `json:"waiting_seconds"`
}

// AnsweringResponse is GET /v1/routing/answering: whether agents are answering
// people over one window. Turns says what the dispatcher did; Waiting says what
// the people got. Both are needed — a fleet can finish every turn and still
// answer nobody, and a chat can wait with no turn ever having been due.
type AnsweringResponse struct {
	Since   string        `json:"since"`
	Turns   TurnOutcomes  `json:"turns"`
	Waiting []WaitingChat `json:"waiting"`
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

// CostResponse is GET /v1/cost?turn_id=: what the model gateway charged the key
// that ran the turn. 404 when no meter holds a number for it — the turn ran with
// no gateway, or the gateway has not flushed its usage rows yet.
type CostResponse struct {
	TurnID       string `json:"turn_id"`
	Folder       string `json:"folder"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CostCents    int    `json:"cost_cents"`
}

// FolderSpend is one folder's metered usage in a CostSummaryResponse.
type FolderSpend struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// CostSummaryResponse is GET /v1/cost/summary: what the model gateway metered
// per folder over the window.
//
// Metered false means this instance names no gateway and therefore has no spend
// meter at all (spec 6/39). It is carried explicitly because an empty Folders
// list would otherwise read as "nothing was spent", which is a claim an
// unmetered instance cannot make.
type CostSummaryResponse struct {
	Metered bool          `json:"metered"`
	Folders []FolderSpend `json:"folders"`
}

// Err is the uniform JSON error envelope.
type Err struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
