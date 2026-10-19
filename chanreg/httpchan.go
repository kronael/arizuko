package chanreg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// ErrUnsupported is returned when an adapter responds 501 to a social
// verb. Aliased to chanlib.ErrUnsupported so errors.Is chains through
// *chanlib.UnsupportedError values too.
var ErrUnsupported = chanlib.ErrUnsupported

// decodeUnsupported reads a 501 body into a structured *chanlib.UnsupportedError.
// On parse failure or empty body, returns plain ErrUnsupported.
func decodeUnsupported(body io.Reader, fallbackTool, fallbackPlatform string) error {
	var b struct {
		Tool     string `json:"tool"`
		Platform string `json:"platform"`
		Hint     string `json:"hint"`
	}
	if body == nil {
		return ErrUnsupported
	}
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		return ErrUnsupported
	}
	if b.Hint == "" && b.Tool == "" && b.Platform == "" {
		return ErrUnsupported
	}
	if b.Tool == "" {
		b.Tool = fallbackTool
	}
	if b.Platform == "" {
		b.Platform = fallbackPlatform
	}
	return &chanlib.UnsupportedError{Tool: b.Tool, Platform: b.Platform, Hint: b.Hint}
}

// ErrPermanent marks a 4xx adapter response. The adapter refuses the message for
// a reason that a retry cannot repair: a bad request, a deleted chat, or a bad
// emoji. The wrapped error text reaches the agent. routd dead-letters such a row
// at once and retries only a transient failure (`routd/loop.go`
// maybeRetryOutbound). Aliased to chanlib.ErrPermanent so the MCP wall
// classifies by the same sentinel.
var ErrPermanent = chanlib.ErrPermanent

// classifyStatus wraps a non-200 adapter status: 4xx → ErrPermanent, else plain.
func classifyStatus(status int) error {
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w: status %d", ErrPermanent, status)
	}
	return fmt.Errorf("status %d", status)
}

// adapterErr classifies a non-200 send response and carries the reason the
// adapter's `{"error": ...}` body names. Without it a Discord refusal reached
// routd and the agent as a bare "status 502", and the journal held no record
// of why (sloth 2026-09-05).
func adapterErr(resp *http.Response) error {
	err := classifyStatus(resp.StatusCode)
	var body struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil && body.Error != "" {
		return fmt.Errorf("%w: %s", err, body.Error)
	}
	return err
}

type HTTPChannel struct {
	entry *Entry
	// bearer yields the credential presented to the adapter on every call: routd's
	// service:routd ES256 JWT (spec 5/1), or nothing in local dev (no AUTHD_URL).
	// Never nil — NewHTTPChannel installs a getter.
	bearer func(context.Context) (string, error)
	client *http.Client
}

// outMsg carries one outbound request from Send/SendFile into trySend. The
// channel keeps no copy of it: routd owns the durable record and the retry.
type outMsg struct {
	JID            string
	Content        string
	ReplyTo        string
	ThreadID       string
	ThreadRoot     string
	IdempotencyKey string
	Options        []chanlib.Option
	IsFile         bool
	Path           string
	Name           string
	Caption        string
	FileReplyTo    string
}

// NewHTTPChannel builds the routd-side egress client for an adapter. bearer
// yields the service:routd token presented on every call. A nil bearer means
// "no auth header" (local dev with no AUTHD_URL, or single-process tests).
func NewHTTPChannel(e *Entry, bearer func(context.Context) (string, error)) *HTTPChannel {
	if bearer == nil {
		bearer = func(context.Context) (string, error) { return "", nil }
	}
	return &HTTPChannel{
		entry:  e,
		bearer: bearer,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// authHeader sets Authorization from the bearer getter when it yields a token.
// A getter error is logged and the request goes out unauthenticated → the
// adapter returns 401, surfacing the auth failure rather than masking it.
func (h *HTTPChannel) authHeader(ctx context.Context, req *http.Request) {
	tok, err := h.bearer(ctx)
	if err != nil {
		slog.Error("channel egress: bearer token unavailable", "channel", h.entry.Name, "err", err)
		return
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func (h *HTTPChannel) Send(jid, text, replyTo, threadID, threadRoot, idempotencyKey string) (string, error) {
	return h.SendCtx(context.Background(), jid, text, replyTo, threadID, threadRoot, idempotencyKey)
}

func (h *HTTPChannel) SendCtx(ctx context.Context, jid, text, replyTo, threadID, threadRoot, idempotencyKey string) (string, error) {
	return h.SendOptionsCtx(ctx, jid, text, nil, replyTo, threadID, threadRoot, idempotencyKey)
}

// SendOptionsCtx is Send carrying pickable answers (spec 5/19). It is the ONE
// fold: an adapter without `buttons` never receives options, it receives the
// answers written into the text as what the reader types.
func (h *HTTPChannel) SendOptionsCtx(ctx context.Context, jid, text string, options []chanlib.Option,
	replyTo, threadID, threadRoot, idempotencyKey string) (string, error) {
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	if !h.entry.HasCap("buttons") {
		text, options = foldOptions(text, options), nil
	}
	m := outMsg{JID: jid, Content: text, Options: options, ReplyTo: replyTo,
		ThreadID: threadID, ThreadRoot: threadRoot, IdempotencyKey: idempotencyKey}
	return h.trySend(ctx, m)
}

// foldOptions appends one line per answer: what the reader types, then its label.
func foldOptions(text string, options []chanlib.Option) string {
	for _, o := range options {
		text += "\n" + o.Send() + "   — " + o.Label
	}
	return text
}

// trySend makes ONE delivery attempt for m and returns the result to the caller.
// The channel holds no queue: routd writes the bot row `pending` before the
// attempt, and its 30s sweep re-sends a row that stays `pending`. A retry here
// would be a second writer beside that sweep.
func (h *HTTPChannel) trySend(ctx context.Context, m outMsg) (string, error) {
	if m.IsFile {
		if !h.entry.HasCap("send_file") {
			return "", fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
		}
		resp, err := h.uploadMultipart(ctx, "/send-file", m.JID, m.Path, m.Name, m.Caption, m.FileReplyTo, m.ThreadID, m.IdempotencyKey)
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == http.StatusOK {
				return decodeSentID(resp.Body, h.entry.Name, "send-file"), nil
			}
			err = adapterErr(resp)
		}
		return "", fmt.Errorf("channel %s send-file: %w", h.entry.Name, err)
	}
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	body := map[string]any{"chat_jid": m.JID, "content": m.Content}
	if len(m.Options) > 0 {
		body["options"] = m.Options
	}
	if m.ReplyTo != "" {
		body["reply_to"] = m.ReplyTo
	}
	if m.ThreadID != "" {
		body["thread_id"] = m.ThreadID
	}
	if m.ThreadRoot != "" {
		body["thread_root"] = m.ThreadRoot
	}
	if m.IdempotencyKey != "" {
		body["idempotency_key"] = m.IdempotencyKey
	}
	b, _ := json.Marshal(body)
	httpResp, err := h.post(ctx, "/send", b)
	if err == nil {
		defer func() { _ = httpResp.Body.Close() }()
		if httpResp.StatusCode == http.StatusOK {
			return decodeSentID(httpResp.Body, h.entry.Name, "send"), nil
		}
		err = adapterErr(httpResp)
	}
	return "", fmt.Errorf("channel %s send: %w", h.entry.Name, err)
}

func (h *HTTPChannel) SendFile(jid, path, name, caption, replyTo, threadID, idempotencyKey string) (string, error) {
	return h.SendFileCtx(context.Background(), jid, path, name, caption, replyTo, threadID, idempotencyKey)
}

func (h *HTTPChannel) SendFileCtx(ctx context.Context, jid, path, name, caption, replyTo, threadID, idempotencyKey string) (string, error) {
	if !h.entry.HasCap("send_file") {
		return "", fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
	}
	m := outMsg{JID: jid, IsFile: true, Path: path, Name: name, Caption: caption, FileReplyTo: replyTo, ThreadID: threadID, IdempotencyKey: idempotencyKey}
	return h.trySend(ctx, m)
}

func (h *HTTPChannel) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	return h.SendVoiceCtx(context.Background(), jid, audioPath, caption, threadID)
}

func (h *HTTPChannel) SendVoiceCtx(ctx context.Context, jid, audioPath, caption, threadID string) (string, error) {
	if !h.entry.HasCap("send_voice") {
		return "", chanlib.Unsupported("send_voice", h.entry.Name, "adapter does not advertise voice capability")
	}
	resp, err := h.uploadMultipart(ctx, "/send-voice", jid, audioPath, filepath.Base(audioPath), caption, "", threadID, "")
	if err != nil {
		return "", fmt.Errorf("channel %s send-voice: %w", h.entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "send_voice", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s send-voice: status %d", h.entry.Name, resp.StatusCode)
	}
	return decodeSentID(resp.Body, h.entry.Name, "send-voice"), nil
}

// decodeSentID reads the platform id an adapter returns for a message it
// delivered. The id is optional, but a body that will not parse leaves the row
// with none, and reply/like/delete address a message by exactly that id.
func decodeSentID(body io.Reader, channel, verb string) string {
	var r struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(body).Decode(&r); err != nil {
		slog.Warn("channel response carries no readable id; later reply/like/delete cannot target this message",
			"channel", channel, "verb", verb, "err", err)
	}
	return r.ID
}

// uploadMultipart builds a multipart body and POSTs to endpoint.
func (h *HTTPChannel) uploadMultipart(ctx context.Context, endpoint, jid, path, name, caption, replyTo, threadID, idempotencyKey string) (*http.Response, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// chat_jid and filename go out even when empty — the adapters read both by
	// name. The rest are omitted when unset, which is what tells an adapter the
	// send carries no caption, no reply target and no thread.
	fields := [][2]string{{"chat_jid", jid}, {"filename", name}}
	for _, kv := range [][2]string{
		{"idempotency_key", idempotencyKey}, {"caption", caption},
		{"reply_to", replyTo}, {"thread_id", threadID},
	} {
		if kv[1] != "" {
			fields = append(fields, kv)
		}
	}
	for _, kv := range fields {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("write %s field: %w", kv[0], err)
		}
	}
	formName := name
	if formName == "" {
		formName = filepath.Base(path)
	}
	fw, err := w.CreateFormFile("file", formName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(fw, f); err != nil {
		return nil, fmt.Errorf("copy file: %w", err)
	}
	// Closing writes the terminating boundary: without it the body is not a
	// valid multipart document and the adapter rejects the upload.
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", h.entry.URL+endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	h.authHeader(ctx, req)
	return h.client.Do(req)
}

func (h *HTTPChannel) Typing(jid string, on bool) error {
	if !h.entry.HasCap("typing") {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"chat_jid": jid, "on": on})
	resp, err := h.post(context.Background(), "/typing", b)
	if err != nil {
		slog.Warn("typing request failed", "channel", h.entry.Name, "jid", jid, "err", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("typing: non-2xx response", "channel", h.entry.Name, "status", resp.StatusCode)
	}
	return nil
}

// PostRoundDone signals the channel that the agent's run for turnID has closed.
// chatJID is the turn's chat JID verbatim (the SSE hub key, BUGS L30).
// 404 is treated as "channel doesn't implement round-handle protocol" — not an error.
func (h *HTTPChannel) PostRoundDone(chatJID, turnID, status, errMsg string) error {
	body := map[string]string{"chat_jid": chatJID, "turn_id": turnID, "status": status}
	if errMsg != "" {
		body["error"] = errMsg
	}
	b, _ := json.Marshal(body)
	resp, err := h.post(context.Background(), "/v1/round_done", b)
	if err != nil {
		return fmt.Errorf("channel %s round_done: %w", h.entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s round_done: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// FetchHistory proxies to the adapter's GET /v1/history endpoint.
// Returns an error if the adapter doesn't advertise the fetch_history cap.
func (h *HTTPChannel) FetchHistory(ctx context.Context, jid string, before time.Time, limit int) ([]byte, error) {
	if !h.entry.HasCap("fetch_history") {
		return nil, fmt.Errorf("channel %s: fetch_history not supported", h.entry.Name)
	}
	u := fmt.Sprintf("%s/v1/history?jid=%s&limit=%d", h.entry.URL, jid, limit)
	if !before.IsZero() {
		u += "&before=" + before.UTC().Format(time.RFC3339)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	h.authHeader(ctx, req)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("channel %s fetch_history: %w", h.entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("channel %s fetch_history: status %d", h.entry.Name, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// postVerb posts JSON to endpoint, handles 501→ErrUnsupported, non-200→error,
// and decodes an optional "id" field from the response. Used by social verbs.
func (h *HTTPChannel) postVerb(ctx context.Context, verb, endpoint string, body []byte) (string, error) {
	resp, err := h.post(ctx, endpoint, body)
	if err != nil {
		return "", fmt.Errorf("channel %s %s: %w", h.entry.Name, verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, verb, h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s %s: %w", h.entry.Name, verb, adapterErr(resp))
	}
	return decodeSentID(resp.Body, h.entry.Name, verb), nil
}

func (h *HTTPChannel) Post(ctx context.Context, jid, content string, mediaPaths []string) (string, error) {
	if !h.entry.HasCap("post") {
		return "", chanlib.Unsupported("post", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]any{"chat_jid": jid, "content": content}
	if len(mediaPaths) > 0 {
		body["media_paths"] = mediaPaths
	}
	b, _ := json.Marshal(body)
	return h.postVerb(ctx, "post", "/post", b)
}

func makeBody(fields map[string]string) []byte {
	b, _ := json.Marshal(fields)
	return b
}

func (h *HTTPChannel) Like(ctx context.Context, jid, targetID, reaction string) error {
	if !h.entry.HasCap("like") {
		return chanlib.Unsupported("like", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "like", "/like", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID, "reaction": reaction}))
	return err
}

func (h *HTTPChannel) Delete(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("delete") {
		return chanlib.Unsupported("delete", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "delete", "/delete", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Pin(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("pin") {
		return chanlib.Unsupported("pin", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "pin", "/pin", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Unpin(ctx context.Context, jid, targetID string, all bool) error {
	if !h.entry.HasCap("pin") {
		return chanlib.Unsupported("unpin", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]any{"chat_jid": jid}
	if all {
		body["all"] = true
	} else {
		body["target_id"] = targetID
	}
	b, _ := json.Marshal(body)
	_, err := h.postVerb(ctx, "unpin", "/unpin", b)
	return err
}

func (h *HTTPChannel) Forward(ctx context.Context, sourceMsgID, targetJID, comment string) (string, error) {
	if !h.entry.HasCap("fwd") {
		return "", chanlib.Unsupported("forward", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]string{"source_msg_id": sourceMsgID, "target_jid": targetJID}
	if comment != "" {
		body["comment"] = comment
	}
	return h.postVerb(ctx, "forward", "/forward", makeBody(body))
}

func (h *HTTPChannel) Quote(ctx context.Context, jid, sourceMsgID, comment string) (string, error) {
	if !h.entry.HasCap("quote") {
		return "", chanlib.Unsupported("quote", h.entry.Name, "adapter does not advertise capability")
	}
	return h.postVerb(ctx, "quote", "/quote", makeBody(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID, "comment": comment}))
}

func (h *HTTPChannel) Repost(ctx context.Context, jid, sourceMsgID string) (string, error) {
	if !h.entry.HasCap("repost") {
		return "", chanlib.Unsupported("repost", h.entry.Name, "adapter does not advertise capability")
	}
	return h.postVerb(ctx, "repost", "/repost", makeBody(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID}))
}

func (h *HTTPChannel) Dislike(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("dislike") {
		return chanlib.Unsupported("dislike", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "dislike", "/dislike", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Edit(ctx context.Context, jid, targetID, content string) error {
	if !h.entry.HasCap("edit") {
		return chanlib.Unsupported("edit", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "edit", "/edit", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID, "content": content}))
	return err
}

// SetSuggestions targets the adapter's /v1/pane/prompts endpoint
// (Slack-only today; spec 8/D-slack-agent-pane). Adapters without the capability
// respond 404 → ErrUnsupported. Idempotent; one-shot per outbound.
func (h *HTTPChannel) SetSuggestions(ctx context.Context, jid string, prompts []core.PanePrompt) error {
	b, _ := json.Marshal(map[string]any{"jid": jid, "prompts": prompts})
	resp, err := h.post(ctx, "/v1/pane/prompts", b)
	if err != nil {
		return fmt.Errorf("channel %s pane_set_prompts: %w", h.entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return chanlib.Unsupported("pane_set_prompts", h.entry.Name, "adapter has no open pane for jid (or doesn't support suggestions)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s pane_set_prompts: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) SetName(ctx context.Context, jid, name string) error {
	b, _ := json.Marshal(map[string]string{"jid": jid, "title": name})
	resp, err := h.post(ctx, "/v1/pane/title", b)
	if err != nil {
		return fmt.Errorf("channel %s pane_set_title: %w", h.entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return chanlib.Unsupported("pane_set_title", h.entry.Name, "adapter has no open pane for jid (or doesn't support rename)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s pane_set_title: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", h.entry.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	h.authHeader(ctx, req)
	return h.client.Do(req)
}
