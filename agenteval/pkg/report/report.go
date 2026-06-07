// Package report renders agenteval run results as JSON and markdown.
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Result is the outcome of one case.
type Result struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Pass      bool   `json:"pass"`
	Reason    string `json:"reason"`
	LatencyMs int64  `json:"latency_ms"`
	Tokens    int    `json:"tokens"`
}

// Summary aggregates results for the report header and exit decision.
type Summary struct {
	Total  int               `json:"total"`
	Passed int               `json:"passed"`
	Tokens int               `json:"tokens"`
	ByDim  map[string][2]int `json:"by_dim"` // dim -> [passed, total]
}

// Summarize folds results into a Summary.
func Summarize(rs []Result) Summary {
	s := Summary{ByDim: map[string][2]int{}}
	for _, r := range rs {
		s.Total++
		s.Tokens += r.Tokens
		d := s.ByDim[r.Dimension]
		d[1]++
		if r.Pass {
			s.Passed++
			d[0]++
		}
		s.ByDim[r.Dimension] = d
	}
	return s
}

// AllPassed reports whether every case passed (drives the process exit code).
func AllPassed(rs []Result) bool {
	for _, r := range rs {
		if !r.Pass {
			return false
		}
	}
	return true
}

// JSON is the machine-readable report.
func JSON(rs []Result) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"summary": Summarize(rs),
		"results": rs,
	}, "", "  ")
}

// Markdown is the human summary: per-dimension pass rate + a case table.
func Markdown(rs []Result) string {
	s := Summarize(rs)
	var b strings.Builder
	fmt.Fprintf(&b, "# agenteval — %d/%d passed (%d tokens)\n\n", s.Passed, s.Total, s.Tokens)
	dims := make([]string, 0, len(s.ByDim))
	for d := range s.ByDim {
		dims = append(dims, d)
	}
	sort.Strings(dims)
	for _, d := range dims {
		fmt.Fprintf(&b, "- %s: %d/%d\n", d, s.ByDim[d][0], s.ByDim[d][1])
	}
	b.WriteString("\n| case | dim | result | ms | tokens | reason |\n")
	b.WriteString("|------|-----|--------|----|--------|--------|\n")
	for _, r := range rs {
		mark := "FAIL"
		if r.Pass {
			mark = "pass"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %s |\n",
			r.ID, r.Dimension, mark, r.LatencyMs, r.Tokens, r.Reason)
	}
	return b.String()
}
