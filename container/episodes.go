package container

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// readEpisodeSummary extracts the `summary:` and `type:` values from a markdown file's
// YAML frontmatter. Returns empty strings if not found.
func readEpisodeSummary(path string) (summary, epType string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return "", ""
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return "", ""
	}
	fm := content[3 : end+3]
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "summary:") {
			summary = strings.TrimSpace(strings.TrimPrefix(line, "summary:"))
			summary = strings.Trim(summary, `"'`)
		}
		if strings.HasPrefix(line, "type:") {
			epType = strings.TrimSpace(strings.TrimPrefix(line, "type:"))
			epType = strings.Trim(epType, `"'`)
		}
	}
	return
}

// ReadRecentEpisodes reads episodes from groupDir/episodes/, returns XML annotation.
// Returns "" if directory missing or empty.
func ReadRecentEpisodes(groupDir string) string {
	dir := filepath.Join(groupDir, "episodes")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}

	type ep struct {
		key     string // filename without extension
		epType  string
		summary string
	}

	byType := map[string][]ep{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		summary, epType := readEpisodeSummary(filepath.Join(dir, e.Name()))
		if summary == "" || epType == "" {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".md")
		byType[epType] = append(byType[epType], ep{key, epType, summary})
	}

	if len(byType) == 0 {
		return ""
	}

	// sort each type descending by filename (ISO date format sorts correctly)
	var all []ep
	for _, eps := range byType {
		sort.Slice(eps, func(i, j int) bool { return eps[i].key > eps[j].key })
		limit := 3
		if len(eps) < limit {
			limit = len(eps)
		}
		all = append(all, eps[:limit]...)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<episodes count=%q>\n", len(all))
	for _, e := range all {
		fmt.Fprintf(&b, "  <entry key=%q type=%q>%s</entry>\n", e.key, e.epType, e.summary)
	}
	b.WriteString("</episodes>")
	return b.String()
}
