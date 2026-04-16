package container

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	lines := strings.Split(fm, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "type:") {
			epType = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "type:")), `"'`)
		}
		if strings.HasPrefix(trimmed, "summary:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "summary:"))
			if val != "" && val != "|" && val != ">" {
				summary = strings.Trim(val, `"'`)
				continue
			}
			// block scalar: collect indented continuation lines
			var parts []string
			for _, bl := range lines[i+1:] {
				if len(bl) == 0 {
					continue
				}
				if bl[0] == ' ' || bl[0] == '\t' {
					parts = append(parts, strings.TrimSpace(bl))
				} else {
					break
				}
			}
			summary = strings.Join(parts, " ")
		}
	}
	return
}

func ReadRecentEpisodes(groupDir string) string {
	dir := filepath.Join(groupDir, "episodes")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}

	type ep struct{ key, epType, summary string }

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

	var all []ep
	for _, eps := range byType {
		sort.Slice(eps, func(i, j int) bool { return eps[i].key > eps[j].key })
		all = append(all, eps[:min(3, len(eps))]...)
	}
	if len(all) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<episodes count=\"%d\">\n", len(all))
	for _, e := range all {
		fmt.Fprintf(&b, "  <entry key=%q type=%q>%s</entry>\n", e.key, e.epType, e.summary)
	}
	b.WriteString("</episodes>")
	return b.String()
}
