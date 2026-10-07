package testutils

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// LLMReply is what the fake model answers every request with until replaced.
// Status >= 400 answers an Anthropic error envelope carrying Message instead
// of a message; the harness then reports the API failure.
type LLMReply struct {
	Text         string
	InputTokens  int
	OutputTokens int
	// CostCents is what this reply cost, which /spend/logs reports back in
	// dollars for the key that made the call. The gateway is the ledger and
	// arizuko stores no spend (spec 6/39), so a test that asserts on cost
	// reads this same number rather than a second copy.
	CostCents int
	Status    int
	Message   string
}

// LLMCall is one request the fake model served, as much of it as a test asserts
// on: which model the harness asked for, the bearer it presented, and how many
// tools it advertised.
type LLMCall struct {
	Path   string
	Model  string
	Stream bool
	Bearer string
	Tools  int
	// The usage this call was answered with, which is what /spend/logs reports
	// back for the key that made it. Spend is dollars, the unit litellm uses.
	InputTokens  int
	OutputTokens int
	Spend        float64
}

// FakeLLM is the Anthropic Messages API stood in by a test: it speaks the wire
// shape the claude CLI expects — SSE when the request streams, one JSON message
// otherwise — and records every call. It listens on every interface so a
// container on the docker bridge reaches it through MODEL_GATEWAY_URL, which is
// the seam every harness already redirects on (spec 6/23 § Wiring); nothing in
// the agent path knows it is talking to a fake.
type FakeLLM struct {
	ln    net.Listener
	mu    sync.Mutex
	reply LLMReply
	calls []LLMCall
	// keys maps sha256(key) to the key the gateway minted, which is how
	// /spend/logs names a key. routd mints one per turn and reads spend back
	// by that hash (spec 6/39), so a fake that serves only /v1/messages
	// answers 404 to the mint and no turn runs at all.
	keys map[string]string
	// owner maps a minted key to the team and user it was minted for, so a call
	// served on that key is credited the way litellm credits it.
	owner map[string]keyOwner
	// teams and users are the LEDGER. This fake meters like the real gateway:
	// serving a call adds its spend to the key's team and user, /team/info
	// reports it, and the budget gate refuses on it. A fake whose spend the
	// TEST wrote instead would prove only that the test can add up.
	teams   map[string]*meterRow
	users   map[string]*meterRow
	minted  int
	deleted []string
}

// keyOwner is the team (folder) and user (caller) a minted key is charged to.
type keyOwner struct{ team, user string }

// meterRow is one team's or user's spend and budget, in dollars, the unit
// litellm speaks on the wire.
type meterRow struct {
	spend     float64
	maxBudget *float64
}

// NewFakeLLM starts the fake on an ephemeral port bound to 0.0.0.0 and closes
// it with the test.
func NewFakeLLM(t *testing.T, reply LLMReply) *FakeLLM {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("fake llm listen: %v", err)
	}
	f := &FakeLLM{ln: ln, reply: reply,
		keys:  map[string]string{},
		owner: map[string]keyOwner{},
		teams: map[string]*meterRow{},
		users: map[string]*meterRow{},
	}
	srv := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Logf("fake llm server stopped: %v", err)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

// Port is the bound TCP port.
func (f *FakeLLM) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// URLFor is the base URL a client on host's network uses to reach the fake —
// the docker bridge gateway for a container, 127.0.0.1 for the test process.
func (f *FakeLLM) URLFor(host string) string {
	return fmt.Sprintf("http://%s:%d", host, f.Port())
}

// SetReply replaces the scripted answer for every call from now on.
func (f *FakeLLM) SetReply(r LLMReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = r
}

// Calls returns a copy of every request served so far, in order.
func (f *FakeLLM) Calls() []LLMCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LLMCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// MintedKeys returns every key the gateway handed out, in mint order.
func (f *FakeLLM) MintedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.keys))
	for _, k := range f.keys {
		out = append(out, k)
	}
	return out
}

// serveGateway answers the admin surface routd drives: reconcile the folder's
// team and the caller's budget, mint a key, revoke it, and report what was
// spent. It is a small LEDGER rather than a set of canned replies — a call
// served on a minted key is credited to that key's team and user — so a test
// asserting on a cap reads the number the metering produced.
func (f *FakeLLM) serveGateway(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/key/generate":
		var body struct {
			TeamID string `json:"team_id"`
			UserID string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.minted++
		key := fmt.Sprintf("sk-fake-turn-%d", f.minted)
		sum := sha256.Sum256([]byte(key))
		f.keys[hex.EncodeToString(sum[:])] = key
		f.owner[key] = keyOwner{team: body.TeamID, user: body.UserID}
		f.mu.Unlock()
		writeJSON(w, map[string]string{"key": key})
		return true

	case r.Method == http.MethodGet && r.URL.Path == "/team/info":
		return f.serveInfo(w, f.teams, r.URL.Query().Get("team_id"), "team_info")

	case r.Method == http.MethodGet && r.URL.Path == "/user/info":
		return f.serveInfo(w, f.users, r.URL.Query().Get("user_id"), "user_info")

	case r.Method == http.MethodPost && r.URL.Path == "/team/new":
		return f.serveUpsert(w, r, f.teams, "team_id", true)
	case r.Method == http.MethodPost && r.URL.Path == "/team/update":
		return f.serveUpsert(w, r, f.teams, "team_id", false)
	case r.Method == http.MethodPost && r.URL.Path == "/user/new":
		return f.serveUpsert(w, r, f.users, "user_id", true)
	case r.Method == http.MethodPost && r.URL.Path == "/user/update":
		return f.serveUpsert(w, r, f.users, "user_id", false)

	case r.Method == http.MethodGet && r.URL.Path == "/spend/logs":
		f.mu.Lock()
		key := f.keys[r.URL.Query().Get("api_key")]
		type row struct {
			Model            string  `json:"model"`
			Spend            float64 `json:"spend"`
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
		}
		rows := []row{}
		for _, c := range f.calls {
			if key == "" || c.Bearer != "Bearer "+key {
				continue
			}
			rows = append(rows, row{
				Model: c.Model, Spend: c.Spend,
				PromptTokens: c.InputTokens, CompletionTokens: c.OutputTokens,
			})
		}
		f.mu.Unlock()
		writeJSON(w, rows)
		return true

	case r.Method == http.MethodGet && r.URL.Path == "/team/daily/activity":
		f.mu.Lock()
		ents := map[string]any{}
		for team := range f.teams {
			in, out, spend := 0, 0, 0.0
			for _, c := range f.calls {
				if f.owner[strings.TrimPrefix(c.Bearer, "Bearer ")].team != team {
					continue
				}
				in += c.InputTokens
				out += c.OutputTokens
				spend += c.Spend
			}
			ents[team] = map[string]any{"metrics": map[string]any{
				"spend": spend, "prompt_tokens": in, "completion_tokens": out,
			}}
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"results": []map[string]any{{
			"date":      time.Now().UTC().Format("2006-01-02"),
			"metrics":   map[string]any{},
			"breakdown": map[string]any{"entities": ents},
		}}})
		return true

	case r.Method == http.MethodPost && r.URL.Path == "/key/delete":
		var body struct {
			Keys []string `json:"keys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.deleted = append(f.deleted, body.Keys...)
		f.mu.Unlock()
		writeJSON(w, map[string]string{"status": "ok"})
		return true
	}
	return false
}

// serveInfo answers /team/info and /user/info: the row nested under its own
// key, or 404 when the gateway holds none — the signal that tells the gate to
// create one.
func (f *FakeLLM) serveInfo(w http.ResponseWriter, rows map[string]*meterRow, id, key string) bool {
	f.mu.Lock()
	row := rows[id]
	var out map[string]any
	if row != nil {
		out = map[string]any{"spend": row.spend, "max_budget": nil}
		if row.maxBudget != nil {
			out["max_budget"] = *row.maxBudget
		}
	}
	f.mu.Unlock()
	if out == nil {
		http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
		return true
	}
	writeJSON(w, map[string]any{key: out})
	return true
}

// serveUpsert is /team/new + /team/update and their user twins. create=true
// refuses a duplicate with 400, as the real /team/new does.
func (f *FakeLLM) serveUpsert(w http.ResponseWriter, r *http.Request, rows map[string]*meterRow, idField string, create bool) bool {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	id, _ := body[idField].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if create && rows[id] != nil {
		http.Error(w, `{"error":"already exists"}`, http.StatusBadRequest)
		return true
	}
	row := rows[id]
	if row == nil {
		row = &meterRow{}
		rows[id] = row
	}
	row.maxBudget = nil
	if v, ok := body["max_budget"].(float64); ok {
		row.maxBudget = &v
	}
	writeJSON(w, map[string]any{idField: id})
	return true
}

// meter credits one served call to the key's team and user, which is what makes
// the next turn's budget gate read a number this fake produced rather than one
// a test wrote. Caller holds f.mu.
func (f *FakeLLM) meter(bearer string, spend float64) {
	o := f.owner[strings.TrimPrefix(bearer, "Bearer ")]
	for _, pair := range []struct {
		id   string
		rows map[string]*meterRow
	}{{o.team, f.teams}, {o.user, f.users}} {
		if pair.id == "" {
			continue
		}
		row := pair.rows[pair.id]
		if row == nil {
			row = &meterRow{}
			pair.rows[pair.id] = row
		}
		row.spend += spend
	}
}

// TeamSpendCents is what the gateway has metered for one folder, in cents —
// the same number its budget gate refuses on.
func (f *FakeLLM) TeamSpendCents(folder string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.teams[folder]
	if row == nil {
		return 0
	}
	return int(row.spend*100 + 0.5)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *FakeLLM) serve(w http.ResponseWriter, r *http.Request) {
	if f.serveGateway(w, r) {
		return
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/messages") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model  string            `json:"model"`
		Stream bool              `json:"stream"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	call := LLMCall{
		Path: r.URL.Path, Model: body.Model, Stream: body.Stream,
		Bearer: r.Header.Get("Authorization"), Tools: len(body.Tools),
		InputTokens: f.reply.InputTokens, OutputTokens: f.reply.OutputTokens,
		Spend: float64(f.reply.CostCents) / 100,
	}
	// A refused call still gets a spend row, carrying status "failure" with zero
	// tokens and zero spend — measured against the real gateway on 2026-09-06.
	// So the meter records that the turn happened and charges it nothing.
	if f.reply.Status >= 400 {
		call.InputTokens, call.OutputTokens, call.Spend = 0, 0, 0
	}
	f.calls = append(f.calls, call)
	// Credit the key's team and user, the way litellm meters a served call.
	f.meter(call.Bearer, call.Spend)
	reply := f.reply
	f.mu.Unlock()

	if reply.Status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.Status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type": "invalid_request_error", "message": reply.Message,
			},
		})
		return
	}
	if body.Stream {
		f.stream(w, body.Model, reply)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": body.Model,
		"content":     []map[string]any{{"type": "text", "text": reply.Text}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens": reply.InputTokens, "output_tokens": reply.OutputTokens,
		},
	})
}

// stream writes the event sequence the SDK reduces into one assistant message:
// usage arrives split, input on message_start and output on message_delta,
// exactly where the client reads it.
func (f *FakeLLM) stream(w http.ResponseWriter, model string, reply LLMReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	ev := func(name string, data any) {
		raw, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		if flush != nil {
			flush.Flush()
		}
	}
	ev("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens": reply.InputTokens, "output_tokens": 1,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			},
		},
	})
	ev("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	ev("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": reply.Text},
	})
	ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	ev("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": reply.OutputTokens},
	})
	ev("message_stop", map[string]any{"type": "message_stop"})
}
