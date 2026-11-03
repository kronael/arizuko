package diary

import (
	"os"
	"strings"
)

func ExtractSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return ""
	}
	frontmatter := content[4 : 4+end]
	lines := strings.Split(frontmatter, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "summary:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "summary:"))
		// inline value
		if val != "" && val != "|" && val != ">" {
			return strings.Trim(val, "\"'")
		}
		// block scalar: collect indented lines that follow
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
		return strings.Join(parts, " ")
	}
	return ""
}
