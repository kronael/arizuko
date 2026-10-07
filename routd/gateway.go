package routd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// The model gateway's admin surface (spec 6/23, 6/39). LiteLLM is the ledger:
// it meters every call, holds the folder's cap as a team budget and the
// person's as an internal user's, and refuses the request itself when either
// is spent. routd mints ONE virtual key per turn, hands it to runed, then
// deletes it; the master key never leaves this daemon. routd mints because the
// coordinates are routd's rows and runed has no store (spec 5/E-routd). Nothing
// comes back to be written down: a turn's cost is the gateway's number.
type modelGateway struct {
	baseURL   string
	masterKey string
	http      *http.Client
}

const (
	// The gateway is a compose neighbour: a call slower than this is not slow,
	// it is gone.
	gatewayCallTimeout = 15 * time.Second
	gatewayMaxBody     = 1 << 20
	// Every budget resets at 00:00 UTC, the window `cost_cap_cents_per_day` is
	// denominated in and the reset the refusal text quotes.
	gatewayDailyBudget = "1d"
)

// errGatewayNotFound is a distinct condition, not an error to log: the absence
// is exactly what tells the gate to create the entity.
var errGatewayNotFound = errors.New("gateway: no such entity")

// newGatewayIfConfigured returns nil when the instance names no gateway. The
// URL alone decides: a URL with no master key mints nothing, which is why
// cmd/routd refuses to boot on that pair.
func newGatewayIfConfigured(baseURL, masterKey string) *modelGateway {
	if baseURL == "" {
		return nil
	}
	return newModelGateway(baseURL, masterKey)
}

func newModelGateway(baseURL, masterKey string) *modelGateway {
	return &modelGateway{
		baseURL:   strings.TrimRight(baseURL, "/"),
		masterKey: masterKey,
		http:      &http.Client{Timeout: gatewayCallTimeout},
	}
}

// turnKey is one turn's virtual credential: Key is what the container
// presents; Hash is sha256(Key), how the gateway's spend log names it.
type turnKey struct {
	Key  string
	Hash string
}

// budget is what the gateway is metering for one folder or person, in cents.
// Cap 0 means the gateway holds no ceiling for this entity.
type budget struct {
	SpentCents int
	CapCents   int
}

// mint asks the gateway for this turn's key, carrying team = the folder and
// user = the caller and no budget of its own — a per-turn ceiling would be a
// third policy the operator never set. ttl 0 leaves the key alive until revoke.
// A key naming a team the gateway lacks is minted happily and then 404s EVERY
// model call, so the caller ensures the team first (spec 6/39).
func (g *modelGateway) mint(
	ctx context.Context, alias, folder, turnID, userSub string, ttl time.Duration,
) (turnKey, error) {
	body := map[string]any{
		"key_alias": alias,
		"team_id":   folder,
		"metadata":  map[string]string{"folder": folder, "turn_id": turnID},
	}
	// An empty user_id would be a key attributed to nobody.
	if userSub != "" {
		body["user_id"] = userSub
	}
	if ttl > 0 {
		body["duration"] = fmt.Sprintf("%ds", int(ttl.Seconds()))
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

// revoke deletes the key; the TTL is the backstop for a routd that dies mid-turn.
func (g *modelGateway) revoke(ctx context.Context, key string) error {
	return g.call(ctx, http.MethodPost, "/key/delete", map[string]any{"keys": []string{key}}, nil)
}

// teamBudget reads a folder's charged spend and cap. errGatewayNotFound means
// the folder has no team yet — the gate's signal to create one.
func (g *modelGateway) teamBudget(ctx context.Context, folder string) (budget, error) {
	var out struct {
		Info struct {
			Spend     float64  `json:"spend"`
			MaxBudget *float64 `json:"max_budget"`
		} `json:"team_info"`
	}
	path := "/team/info?team_id=" + url.QueryEscape(folder)
	if err := g.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return budget{}, err
	}
	return budget{
		SpentCents: dollarsToCents(out.Info.Spend),
		CapCents:   dollarsToCents(deref(out.Info.MaxBudget)),
	}, nil
}

// createTeam gives a folder its team, carrying the operator's cap. The team id
// IS the folder (spec 6/39), so nothing has to be stored to find it again.
func (g *modelGateway) createTeam(ctx context.Context, folder string, capCents int) error {
	body := map[string]any{
		"team_id":    folder,
		"team_alias": folder,
		"metadata":   map[string]string{"folder": folder},
	}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/team/new", body, nil)
}

// updateTeam moves a folder's team onto the cap the operator now holds; the
// gate calls it only when the two disagree.
func (g *modelGateway) updateTeam(ctx context.Context, folder string, capCents int) error {
	body := map[string]any{"team_id": folder}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/team/update", body, nil)
}

// userBudget reads one person's charged spend and cap. errGatewayNotFound is
// the normal state for an uncapped caller.
func (g *modelGateway) userBudget(ctx context.Context, userSub string) (budget, error) {
	var out struct {
		Info struct {
			Spend     float64  `json:"spend"`
			MaxBudget *float64 `json:"max_budget"`
		} `json:"user_info"`
	}
	path := "/user/info?user_id=" + url.QueryEscape(userSub)
	if err := g.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return budget{}, err
	}
	return budget{
		SpentCents: dollarsToCents(out.Info.Spend),
		CapCents:   dollarsToCents(deref(out.Info.MaxBudget)),
	}, nil
}

// createUser holds a person's cap. A key minted with an unknown user_id is
// accepted and metered, so this is needed only for a budget, never for the
// turn to work.
func (g *modelGateway) createUser(ctx context.Context, userSub string, capCents int) error {
	body := map[string]any{"user_id": userSub}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/user/new", body, nil)
}

// updateUser moves a person's cap onto what the operator now holds.
func (g *modelGateway) updateUser(ctx context.Context, userSub string, capCents int) error {
	body := map[string]any{"user_id": userSub}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/user/update", body, nil)
}

// keyUsage is what the gateway charged one key. Requests separates "spent
// nothing" from "no rows yet": the flush is on a timer, so an empty read right
// after a turn is expected rather than an answer.
type keyUsage struct {
	InputTokens  int
	OutputTokens int
	CostCents    int
	Requests     int
}

// keySpend reads what the gateway charged one turn's key, by its hash: litellm
// stores the sha256, never the key, and the hash keeps the credential out of a
// query string every access log records verbatim.
func (g *modelGateway) keySpend(ctx context.Context, keyHash string) (keyUsage, error) {
	var rows []struct {
		Spend            float64 `json:"spend"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
	}
	path := "/spend/logs?api_key=" + url.QueryEscape(keyHash)
	if err := g.call(ctx, http.MethodGet, path, nil, &rows); err != nil {
		return keyUsage{}, err
	}
	out := keyUsage{Requests: len(rows)}
	dollars := 0.0
	for _, r := range rows {
		out.InputTokens += r.PromptTokens
		out.OutputTokens += r.CompletionTokens
		dollars += r.Spend
	}
	// Round the TOTAL once: a turn of sub-cent calls then reports what it cost
	// rather than a column of zeroes.
	out.CostCents = dollarsToCents(dollars)
	return out, nil
}

// folderSpend is one folder's metered usage over an operator's window, in the
// units arizuko's surfaces already speak.
type folderSpend struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// spendByFolder sums /team/daily/activity's per-day rows into per-folder totals
// for [start, end] (YYYY-MM-DD, inclusive). It is the ONLY read behind every
// operator spend surface — dashboard, REST, CLI — so none can show a number
// another one does not (spec 6/39).
func (g *modelGateway) spendByFolder(ctx context.Context, start, end string) ([]folderSpend, error) {
	var out struct {
		Results []struct {
			Breakdown struct {
				Entities map[string]struct {
					Metrics struct {
						Spend            float64 `json:"spend"`
						PromptTokens     int     `json:"prompt_tokens"`
						CompletionTokens int     `json:"completion_tokens"`
					} `json:"metrics"`
				} `json:"entities"`
			} `json:"breakdown"`
		} `json:"results"`
	}
	// page_size bounds ROWS and a row is a day, so a window always fits one page.
	path := fmt.Sprintf("/team/daily/activity?start_date=%s&end_date=%s&page_size=%d",
		url.QueryEscape(start), url.QueryEscape(end), gatewayDailyPageSize)
	if err := g.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	tokens := map[string]int{}
	dollars := map[string]float64{}
	for _, day := range out.Results {
		for folder, ent := range day.Breakdown.Entities {
			tokens[folder] += ent.Metrics.PromptTokens + ent.Metrics.CompletionTokens
			dollars[folder] += ent.Metrics.Spend
		}
	}
	rows := make([]folderSpend, 0, len(tokens))
	for folder, tok := range tokens {
		// Round each folder's TOTAL once, not every day.
		rows = append(rows, folderSpend{
			Folder: folder, Tokens: tok, CostCents: dollarsToCents(dollars[folder]),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Folder < rows[j].Folder })
	return rows, nil
}

// gatewayDailyPageSize bounds the days one spendByFolder read returns; the
// operator windows are weeks, so this is slack.
const gatewayDailyPageSize = 400

// applyBudget writes a cents cap onto a team or user body. Zero is uncapped
// and is sent as an explicit null: clearing a cap has to CLEAR the gateway's,
// and omitting the field would leave the old ceiling enforcing a policy
// arizuko does not hold.
func applyBudget(body map[string]any, capCents int) {
	if capCents > 0 {
		body["max_budget"] = float64(capCents) / 100
		body["budget_duration"] = gatewayDailyBudget
		return
	}
	body["max_budget"] = nil
	body["budget_duration"] = nil
}

// dollarsToCents converts the gateway's unit to arizuko's, rounding once.
func dollarsToCents(d float64) int { return int(math.Round(d * 100)) }

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// call is the one request shape every admin verb shares. A non-200 is an
// error carrying the gateway's own body; a 404 is errGatewayNotFound so a
// caller can tell "no such team" from "no gateway".
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
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, gatewayMaxBody))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", errGatewayNotFound, method, path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
