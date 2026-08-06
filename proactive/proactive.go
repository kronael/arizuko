// Package proactive parses a group's CLAUDE.md `proactive:` frontmatter block
// (spec 5/6). Mode is group business state in the file, not a DB column, so the
// file is the single source and there is nothing to keep in sync.
//
// It lives outside routd because two daemons need the same answer: routd's
// scanner decides whether a group may fire, and dashd's /dash/proactive/ shows
// an operator what that decision will be. A second reader in dashd would drift
// from the one that actually gates the turn, and the drift would surface as a
// dashboard that says `lurk` about a group routd is refusing to run.
package proactive

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode is a group's parsed `proactive:` block. Misconfigured marks a
// present-but-invalid block (logged config error, fires nothing — strict, not
// magical). Absent block → Mode "silent", Misconfigured false.
type Mode struct {
	Name          string // "silent" (default) | "lurk"; "" when Misconfigured
	QuietHours    []Window
	Misconfigured bool
	Err           string
}

// Eligible reports whether the group may fire at all.
func (m Mode) Eligible() bool { return m.Name == "lurk" && !m.Misconfigured }

// Window is one parsed `HH:MM-HH:MM <IANA tz>` entry; a window may cross
// midnight. Raw keeps the operator's own text so a view renders what they
// typed rather than a re-rendering of it.
type Window struct {
	Raw      string
	StartMin int // minutes since 00:00, local to Loc
	EndMin   int
	Loc      *time.Location
}

// Contains reports whether t falls inside the window.
func (q Window) Contains(t time.Time) bool {
	lt := t.In(q.Loc)
	m := lt.Hour()*60 + lt.Minute()
	if q.StartMin <= q.EndMin {
		return m >= q.StartMin && m < q.EndMin
	}
	// crosses midnight: [start,24h) ∪ [0,end)
	return m >= q.StartMin || m < q.EndMin
}

// InQuietHours reports whether t falls inside any of the group's windows.
func (m Mode) InQuietHours(t time.Time) bool {
	for _, w := range m.QuietHours {
		if w.Contains(t) {
			return true
		}
	}
	return false
}

var frontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)
var quietHourRE = regexp.MustCompile(`^(\d{2}):(\d{2})-(\d{2}):(\d{2})\s+(\S+)$`)

// Parse reads a group's CLAUDE.md at path. Absent file or absent block →
// silent (default off). A present-but-invalid block (unknown mode,
// unparseable quiet_hours, bad tz) → Misconfigured (logged error, fires
// nothing) — never silently coerced to silent, because then a typo would look
// exactly like a working setup deliberately turned off.
func Parse(path string) Mode {
	data, err := os.ReadFile(path)
	if err != nil {
		return Mode{Name: "silent"}
	}
	fm := frontmatterRE.FindSubmatch(data)
	if fm == nil {
		return Mode{Name: "silent"}
	}
	var meta struct {
		Proactive *struct {
			Mode       string   `yaml:"mode"`
			QuietHours []string `yaml:"quiet_hours"`
		} `yaml:"proactive"`
	}
	if err := yaml.Unmarshal(fm[1], &meta); err != nil {
		return Mode{Misconfigured: true, Err: "frontmatter yaml: " + err.Error()}
	}
	if meta.Proactive == nil {
		return Mode{Name: "silent"} // absent block → default off
	}
	name := strings.TrimSpace(meta.Proactive.Mode)
	if name == "" {
		name = "silent"
	}
	if name != "silent" && name != "lurk" {
		return Mode{Misconfigured: true, Err: "unknown mode " + name}
	}
	var windows []Window
	for _, raw := range meta.Proactive.QuietHours {
		w, perr := parseWindow(raw)
		if perr != nil {
			return Mode{Misconfigured: true, Err: "quiet_hours " + raw + ": " + perr.Error()}
		}
		windows = append(windows, w)
	}
	return Mode{Name: name, QuietHours: windows}
}

func parseWindow(raw string) (Window, error) {
	trimmed := strings.TrimSpace(raw)
	m := quietHourRE.FindStringSubmatch(trimmed)
	if m == nil {
		return Window{}, fmt.Errorf("want HH:MM-HH:MM <tz>")
	}
	sh, sm := atoiOr(m[1]), atoiOr(m[2])
	eh, em := atoiOr(m[3]), atoiOr(m[4])
	if sh > 23 || eh > 23 || sm > 59 || em > 59 {
		return Window{}, fmt.Errorf("hour/minute out of range")
	}
	loc, err := time.LoadLocation(m[5])
	if err != nil {
		return Window{}, fmt.Errorf("bad tz %q", m[5])
	}
	return Window{Raw: trimmed, StartMin: sh*60 + sm, EndMin: eh*60 + em, Loc: loc}, nil
}

func atoiOr(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}
