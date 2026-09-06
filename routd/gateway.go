package routd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// The model gateway's admin surface (spec 6/23). routd mints ONE virtual key
// per turn, hands it to runed for the container, reads back what the gateway
// charged it, then deletes it. The master key authorises those calls and never
// leaves this daemon: the container holds a credential scoped to its own turn,
// and revoking it is a delete that needs no cooperation from a running spawn.
//
// routd mints because the two numbers the key carries are routd's rows — the
// folder's cap and today's spend — and runed has no store (spec 5/E). The
// spend comes back to the same daemon, which is the only writer of cost_log.
type modelGateway struct {
	baseURL   string
	masterKey string
	http      *http.Client
	// spendFlush bounds the wait for a turn's usage rows to appear on
	// /spend/logs. litellm writes them on a batch timer
	// (proxy_batch_write_at, 1s in the package fragment, plus up to 5s of
	// jitter), so the read taken the instant a container exits sees nothing.
	spendFlush time.Duration
}

const (
	// gatewayCallTimeout bounds one admin call. The gateway is a compose
	// neighbour, so a call that takes longer than this is not slow, it is gone.
	gatewayCallTimeout = 15 * time.Second
	// gatewaySpendFlush is how long the turn boundary waits for the usage rows.
	gatewaySpendFlush = 20 * time.Second
	// gatewaySpendPoll is the interval between those reads.
	gatewaySpendPoll = time.Second
	// gatewayMaxBody caps an admin response read.
	gatewayMaxBody = 1 << 20
)

// newGatewayIfConfigured builds the client when the instance names a gateway,
// and returns nil when it does not. The URL alone decides: a URL with no
// master key mints nothing, which is why routd refuses to boot on that pair
// (cmd/routd) rather than discovering it one failed turn at a time.
func newGatewayIfConfigured(baseURL, masterKey string) *modelGateway {
	if baseURL == "" {
		return nil
	}
	return newModelGateway(baseURL, masterKey)
}

func newModelGateway(baseURL, masterKey string) *modelGateway {
	return &modelGateway{
		baseURL:    strings.TrimRight(baseURL, "/"),
		masterKey:  masterKey,
		http:       &http.Client{Timeout: gatewayCallTimeout},
		spendFlush: gatewaySpendFlush,
	}
}

// turnKey is one turn's virtual credential. Key is what the container
// presents to the gateway; Hash is sha256(Key), which is how the gateway's
// spend log names the key that paid for a request.
type turnKey struct {
	Key  string
	Hash string
}

// mint asks the gateway for this turn's key. budgetCents 0 means the folder is
// uncapped and the key carries no budget — a per-turn ceiling invented here
// would be a policy the operator never set. ttl 0 leaves the key alive until
// revoke deletes it.
func (g *modelGateway) mint(
	ctx context.Context, alias, folder, turnID string, ttl time.Duration, budgetCents int,
) (turnKey, error) {
	body := map[string]any{
		"key_alias": alias,
		"metadata":  map[string]string{"folder": folder, "turn_id": turnID},
	}
	if ttl > 0 {
		body["duration"] = fmt.Sprintf("%ds", int(ttl.Seconds()))
	}
	if budgetCents > 0 {
		body["max_budget"] = float64(budgetCents) / 100
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := g.call(ctx, http.MethodPost, "/key/generate", body, &out); err != nil {
		return turnKey{}, err
	}
	if out.Key == "" {
		return turnKey{}, fmt.Errorf("gateway returned no key for %s", alias)
	}
	sum := sha256.Sum256([]byte(out.Key))
	return turnKey{Key: out.Key, Hash: hex.EncodeToString(sum[:])}, nil
}

// revoke deletes the key. The TTL is the backstop for a routd that dies
// mid-turn; this is the normal end of a key's life.
func (g *modelGateway) revoke(ctx context.Context, key string) error {
	return g.call(ctx, http.MethodPost, "/key/delete", map[string]any{"keys": []string{key}}, nil)
}

// spend returns what the gateway charged this key, one entry per model, in the
// shape cost_log stores. An empty map means the gateway recorded no request —
// a turn that reached no model. The key hash is what /spend/logs matches on
// (litellm stores the sha256, never the key), and passing the hash keeps the
// credential itself out of a query string every access log records verbatim.
func (g *modelGateway) spend(ctx context.Context, keyHash string) (map[string]apiv1.ModelCost, error) {
	var rows []struct {
		Model            string  `json:"model"`
		Spend            float64 `json:"spend"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
	}
	path := "/spend/logs?api_key=" + url.QueryEscape(keyHash)
	if err := g.call(ctx, http.MethodGet, path, nil, &rows); err != nil {
		return nil, err
	}
	dollars := map[string]float64{}
	out := map[string]apiv1.ModelCost{}
	for _, r := range rows {
		model := r.Model
		if model == "" {
			// cost_log's primary key includes the model, so a row the gateway
			// could not name still needs one.
			model = "unknown"
		}
		c := out[model]
		c.Input += r.PromptTokens
		c.Output += r.CompletionTokens
		out[model] = c
		dollars[model] += r.Spend
	}
	// cost_log holds whole cents, so round each model's TOTAL once instead of
	// every request: a turn of many sub-cent calls then records what it cost
	// rather than a column of zeroes.
	for model, d := range dollars {
		c := out[model]
		c.CostCents = int(math.Round(d * 100))
		out[model] = c
	}
	return out, nil
}

// waitForSpend reads the turn's usage once the gateway has flushed it. An
// empty first read is expected, so this polls to the flush bound and then
// reports what it has. An error is a FAILED read and never an empty one: the
// caller must be able to tell "the gateway says this turn spent nothing" from
// "the gateway did not answer", because only the second one means the folder's
// cap is now blind.
func (g *modelGateway) waitForSpend(ctx context.Context, keyHash string) (map[string]apiv1.ModelCost, error) {
	deadline := time.Now().Add(g.spendFlush)
	for {
		out, err := g.spend(ctx, keyHash)
		if err != nil || len(out) > 0 {
			return out, err
		}
		if !time.Now().Before(deadline) {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(gatewaySpendPoll):
		}
	}
}

// call is the one request shape the three admin verbs share. A non-200 is an
// error carrying the gateway's own body: a mint refused for a duplicate alias
// and a mint refused because the database is down are the same status here and
// different problems for whoever reads the log.
func (g *modelGateway) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.masterKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, gatewayMaxBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
