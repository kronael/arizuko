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
// it meters every call, it holds the folder's cap as a team budget and the
// person's as an internal user's, and it refuses the request itself when either
// is spent. routd mints ONE virtual key per turn, hands it to runed for the
// container, then deletes it. The master key authorises those calls and never
// leaves this daemon: the container holds a credential scoped to its own turn,
// and revoking it is a delete that needs no cooperation from a running spawn.
//
// routd mints because the coordinates the key carries are routd's rows — which
// folder the turn runs in and who asked for it — and runed has no store (spec
// 5/E). Nothing comes back to be written down: what a turn cost is the
// gateway's number, read from the gateway when an operator asks for it.
type modelGateway struct {
	baseURL   string
	masterKey string
	http      *http.Client
}

const (
	// gatewayCallTimeout bounds one admin call. The gateway is a compose
	// neighbour, so a call that takes longer than this is not slow, it is gone.
	gatewayCallTimeout = 15 * time.Second
	// gatewayMaxBody caps an admin response read.
	gatewayMaxBody = 1 << 20
	// gatewayDailyBudget resets every budget arizuko sets at 00:00 UTC, which
	// is the window `cost_cap_cents_per_day` is denominated in and the reset
	// the refusal text quotes.
	gatewayDailyBudget = "1d"
)

// errGatewayNotFound is what a read reports for a team or user the gateway does
// not have. It is a distinct condition rather than an error to log, because the
// absence is exactly what tells the gate to create one.
var errGatewayNotFound = errors.New("gateway: no such entity")

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
		baseURL:   strings.TrimRight(baseURL, "/"),
		masterKey: masterKey,
		http:      &http.Client{Timeout: gatewayCallTimeout},
	}
}

// turnKey is one turn's virtual credential. Key is what the container
// presents to the gateway; Hash is sha256(Key), which is how the gateway's
// spend log names the key that paid for a request.
type turnKey struct {
	Key  string
	Hash string
}

// budget is what the gateway is metering for one folder or one person: what it
// has charged in the current window and the ceiling it is enforcing, both in
// cents. Cap 0 means the gateway holds no ceiling for this entity.
type budget struct {
	SpentCents int
	CapCents   int
}

// mint asks the gateway for this turn's key. The key carries the two
// coordinates the meter attributes by: team is the folder, user is the caller.
// It carries no budget of its own — the folder's cap is the team's and the
// person's is the user's, so a ceiling invented per turn would be a third
// policy the operator never set. ttl 0 leaves the key alive until revoke
// deletes it.
//
// A key naming a team the gateway does not have is minted happily and then
// fails EVERY model call with 404, so the caller ensures the team first
// (spec 6/39 § The team must exist before the turn runs).
func (g *modelGateway) mint(
	ctx context.Context, alias, folder, turnID, userSub string, ttl time.Duration,
) (turnKey, error) {
	body := map[string]any{
		"key_alias": alias,
		"team_id":   folder,
		"metadata":  map[string]string{"folder": folder, "turn_id": turnID},
	}
	// An empty user_id would be a key attributed to nobody; the field is left
	// off so the gateway records the turn against its team alone.
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

// revoke deletes the key. The TTL is the backstop for a routd that dies
// mid-turn; this is the normal end of a key's life.
func (g *modelGateway) revoke(ctx context.Context, key string) error {
	return g.call(ctx, http.MethodPost, "/key/delete", map[string]any{"keys": []string{key}}, nil)
}

// teamBudget reads what the gateway has charged a folder in the current window
// and the cap it is enforcing. errGatewayNotFound means the folder has no team
// yet, which is the gate's signal to create one.
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
// IS the folder (spec 6/39 § The mapping), so nothing has to be stored to find
// it again, and the alias carries the folder into the gateway's own UI.
func (g *modelGateway) createTeam(ctx context.Context, folder string, capCents int) error {
	body := map[string]any{
		"team_id":    folder,
		"team_alias": folder,
		"metadata":   map[string]string{"folder": folder},
	}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/team/new", body, nil)
}

// updateTeam moves a folder's team onto the cap the operator now holds. The
// gate calls it only when the two disagree, so an unchanged cap costs no write.
func (g *modelGateway) updateTeam(ctx context.Context, folder string, capCents int) error {
	body := map[string]any{"team_id": folder}
	applyBudget(body, capCents)
	return g.call(ctx, http.MethodPost, "/team/update", body, nil)
}

// userBudget reads what the gateway has charged one person in the current
// window and the cap it is enforcing. errGatewayNotFound means the gateway has
// no row for them, which for an uncapped caller is the normal state.
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

// createUser gives a person their internal-user row carrying their cap. A key
// minted with a user_id the gateway does not know is accepted and metered, so
// this is needed only to hold a budget — never to make the turn work.
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

// keyUsage is what the gateway charged one key. Requests is how many calls it
// found, which is what separates "this turn spent nothing" from "the gateway
// has no rows for it yet" — the flush is on a timer, so an empty read right
// after a turn is expected rather than an answer.
type keyUsage struct {
	InputTokens  int
	OutputTokens int
	CostCents    int
	Requests     int
}

// keySpend reads what the gateway charged one turn's key. The key hash is what
// /spend/logs matches on (litellm stores the sha256, never the key), and passing
// the hash keeps the credential itself out of a query string every access log
// records verbatim.
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
	// Round the TOTAL once rather than every request: a turn of many sub-cent
	// calls then reports what it cost rather than a column of zeroes.
	out.CostCents = dollarsToCents(dollars)
	return out, nil
}

// folderSpend is one folder's metered usage over the window an operator asked
// about. It is the gateway's number, rendered in the units arizuko's surfaces
// already speak.
type folderSpend struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// spendByFolder reads what the gateway metered per folder between two dates
// (YYYY-MM-DD, inclusive). /team/daily/activity reports a row per day with each
// team's share under breakdown.entities, so the days are summed here into the
// per-folder totals the operator surfaces show.
//
// This is the ONLY read behind every operator spend surface: the dashboard, the
// REST face and the CLI all reach the meter through it, so none of them can
// show a number another one does not (spec 6/39).
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
	// page_size bounds ROWS, and a row is a day, so a window's worth of days
	// always fits in one page.
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
		// Round each folder's TOTAL once rather than every day: a folder of many
		// sub-cent days then reports what it cost rather than a column of zeroes.
		rows = append(rows, folderSpend{
			Folder: folder, Tokens: tok, CostCents: dollarsToCents(dollars[folder]),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Folder < rows[j].Folder })
	return rows, nil
}

// gatewayDailyPageSize bounds the days one spendByFolder read returns. The
// operator windows are weeks, so this is slack rather than a limit.
const gatewayDailyPageSize = 400

// applyBudget writes a cents cap onto a team or user body. Zero is uncapped and
// is sent as an explicit null, because the operator clearing a cap has to CLEAR
// the gateway's — omitting the field would leave the old ceiling enforcing a
// policy arizuko no longer holds.
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

// call is the one request shape every admin verb shares. A non-200 is an error
// carrying the gateway's own body: a mint refused for a duplicate alias and a
// mint refused because the database is down are the same status here and
// different problems for whoever reads the log. A 404 is reported as
// errGatewayNotFound so a caller can tell "no such team" from "no gateway".
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
