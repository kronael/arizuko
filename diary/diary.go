package diary

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func Read(groupDir string, max int) string {
	dir := filepath.Join(groupDir, "diary")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return ""
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) > max {
		files = files[:max]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<knowledge layer=\"diary\" count=\"%d\">\n", len(files))
	for _, f := range files {
		key := strings.TrimSuffix(f, ".md")
		age := ageLabel(key, time.Now())
		summary := ExtractSummary(filepath.Join(dir, f))
		if summary == "" {
			continue
		}
		fmt.Fprintf(&b, "  <entry key=%q age=%q>%s</entry>\n",
			key, age, summary)
	}
	b.WriteString("</knowledge>")
	return b.String()
}

func ageLabel(key string, now time.Time) string {
	if key == now.Format("20060102") {
		return "today"
	}
	if key == now.AddDate(0, 0, -1).Format("20060102") {
		return "yesterday"
	}
	t, err := time.Parse("20060102", key)
	if err != nil {
		return "unknown"
	}
	days := int(now.Sub(t).Hours() / 24)
	if days < 7 {
		return fmt.Sprintf("%d days ago", days)
	}
	weeks := days / 7
	if weeks == 1 {
		return "last week"
	}
	return fmt.Sprintf("%d weeks ago", weeks)
}

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
