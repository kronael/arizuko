package diary

import (
	"fmt"
	"log/slog"
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

	now := time.Now()
	today := now.Format("20060102")
	yesterday := now.AddDate(0, 0, -1).Format("20060102")

	var b strings.Builder
	fmt.Fprintf(&b, "<knowledge layer=\"diary\" count=\"%d\">\n", len(files))
	for _, f := range files {
		key := strings.TrimSuffix(f, ".md")
		age := ageLabel(key, today, yesterday, now)
		summary := extractSummary(filepath.Join(dir, f))
		if summary == "" {
			continue
		}
		fmt.Fprintf(&b, "  <entry key=%q age=%q>%s</entry>\n",
			key, age, summary)
	}
	b.WriteString("</knowledge>")
	return b.String()
}

func WriteRecovery(groupDir, reason, errMsg string) {
	now := time.Now()
	dir := filepath.Join(groupDir, "diary")
	os.MkdirAll(dir, 0755)
	file := filepath.Join(dir, now.Format("20060102")+".md")
	_, err := os.Stat(file)
	var entry string
	if err != nil {
		entry = fmt.Sprintf("---\nsummary: \"session ended: %s\"\n---\n", reason)
	}
	entry += fmt.Sprintf("\n## Recovery (%s)\nReason: %s\nError: %s\n",
		now.Format("15:04"), reason, errMsg)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("write recovery diary failed", "err", err)
		return
	}
	defer f.Close()
	f.WriteString(entry)
	slog.Info("wrote recovery diary entry", "group", groupDir, "reason", reason)
}

func ageLabel(key, today, yesterday string, now time.Time) string {
	if key == today {
		return "today"
	}
	if key == yesterday {
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

func extractSummary(path string) string {
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
