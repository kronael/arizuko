// Package spec defines anteval's declarative case schema and loader.
// A case is data: a task prompt for the live agent plus the public-surface
// check that decides pass/fail. No Go edit is needed to add a capability test.
package spec

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Case is one capability probe. Identifiers in Prompt/Check are templated
// ({nonce}, {sink}, {target}, {cb.<key>}) and expanded per run so concurrent
// and repeated runs never collide.
type Case struct {
	ID        string `toml:"id"`
	Dimension string `toml:"dimension"`
	Smoke     bool   `toml:"smoke"`
	Prompt    string `toml:"prompt"`
	Check     Check  `toml:"check"`
	MaxTokens int    `toml:"max_tokens"` // budget: a case spending more than this fails
	MaxWallMs int    `toml:"max_wall_ms"`
}

// Check is a single public-surface assertion on the observable effect.
type Check struct {
	Kind string `toml:"kind"` // callback|http_status|rest_reply|rest_observe|mcp_roundtrip|parity_sentinel
	URL  string `toml:"url"`  // http_status: templated URL to GET
	Want int    `toml:"want"` // http_status: expected code
	Chat string `toml:"chat"` // rest_*/mcp/parity: chat ref (templated)
	Text string `toml:"text"` // rest_*/mcp: substring to find (templated; default {nonce})
}

type file struct {
	Case []Case `toml:"case"`
}

var checkKinds = map[string]bool{
	"callback": true, "http_status": true, "rest_reply": true,
	"rest_observe": true, "mcp_roundtrip": true, "parity_sentinel": true,
}

// Load reads every *.toml in dir; each holds one or more [[case]] blocks.
// Cases come back sorted by ID for stable runs, validated so a typo fails the
// load rather than a run.
func Load(dir string) ([]Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, p := range paths {
		var f file
		if _, err := toml.DecodeFile(p, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		cases = append(cases, f.Case...)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.ID] {
			return nil, fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if err := Validate(c); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

// Validate rejects a malformed case.
func Validate(c Case) error {
	if c.ID == "" {
		return fmt.Errorf("case with empty id")
	}
	if c.Prompt == "" {
		return fmt.Errorf("case %s: empty prompt", c.ID)
	}
	if !checkKinds[c.Check.Kind] {
		return fmt.Errorf("case %s: unknown check kind %q", c.ID, c.Check.Kind)
	}
	if c.Check.Kind == "http_status" && (c.Check.URL == "" || c.Check.Want == 0) {
		return fmt.Errorf("case %s: http_status needs url+want", c.ID)
	}
	return nil
}

// Filter selects by smoke flag and/or dimension/id; empty selectors pass all.
func Filter(cases []Case, smoke bool, dim, id string) []Case {
	var out []Case
	for _, c := range cases {
		if smoke && !c.Smoke {
			continue
		}
		if dim != "" && c.Dimension != dim {
			continue
		}
		if id != "" && c.ID != id {
			continue
		}
		out = append(out, c)
	}
	return out
}
