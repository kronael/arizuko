package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Installed fragments drift from the catalog. `<dataDir>/services/*.yml` are
// COPIES of `template/services/*.yml` — `packages add` copies once and nothing
// ever refreshes them, so a template edit reaches NEW installs only. That is how
// the E1 adapter-state mounts stayed absent on every running instance while the
// template already carried them (BUGS R1). Generate reports the drift on every
// run; `arizuko generate <inst> --sync-services` applies it.
//
// Fragments are matched to the catalog by SERVICE KIND, not filename: a
// multi-account variant `teled-rhias.yml` maps to the `teled` template
// (envFileName uses the same `<kind>-<label>` split). The filename is also what
// decides whether a fragment is replaceable — see FragmentState.

// FragmentState classifies one installed fragment against the bundled catalog.
type FragmentState string

const (
	// FragmentCurrent: every significant line matches its template. Nothing to do.
	FragmentCurrent FragmentState = "current"
	// FragmentStale: `<kind>.yml` whose content differs from `<kind>.yml` in the
	// catalog. The filename says "plain copy of the shipped package", so sync
	// replaces it (previous content kept as `<kind>.yml.bak`).
	FragmentStale FragmentState = "stale"
	// FragmentVariant: `<kind>-<label>.yml`. The operator authored it — it carries
	// its own container_name/CHANNEL_NAME and replacing it with the base template
	// would collide with the base service. NEVER written; only reported, so the
	// operator can apply the catalog's changes by hand.
	FragmentVariant FragmentState = "variant"
	// FragmentLocal: no template of that kind ships. Not ours; silent.
	FragmentLocal FragmentState = "local"
)

// FragmentDrift is one installed fragment's standing against the catalog.
type FragmentDrift struct {
	File  string // installed filename, e.g. "teled-rhias.yml"
	Kind  string // catalog template it maps to, e.g. "teled" ("" when local)
	State FragmentState
	// Missing are catalog lines absent from the installed copy; Extra are
	// installed lines the catalog no longer has (a retired CHANNEL_SECRET, or a
	// variant's own identity lines). Both empty when current or local.
	Missing []string
	Extra   []string
}

// Stale reports whether sync would rewrite this fragment.
func (d FragmentDrift) Stale() bool { return d.State == FragmentStale }

// PlanFragmentSync compares every installed fragment in servicesDir against the
// bundled catalog in tmplDir. Read-only: this is the dry run behind both the
// warning Generate prints and the --sync-services apply.
func PlanFragmentSync(servicesDir, tmplDir string) ([]FragmentDrift, error) {
	names, err := readFragments(servicesDir)
	if err != nil {
		return nil, err
	}
	var out []FragmentDrift
	for _, name := range names {
		kind, tmpl, ok := catalogTemplate(tmplDir, name)
		if !ok {
			out = append(out, FragmentDrift{File: name + ".yml", State: FragmentLocal})
			continue
		}
		installed, err := os.ReadFile(filepath.Join(servicesDir, name+".yml"))
		if err != nil {
			return nil, fmt.Errorf("read services/%s.yml: %w", name, err)
		}
		d := FragmentDrift{File: name + ".yml", Kind: kind, State: FragmentCurrent}
		// Classification compares every significant line, so a changed VALUE (an
		// image tag, a URL) counts as behind; only comments are ignored.
		switch {
		case name != kind:
			d.State = FragmentVariant
		case !slices.Equal(significantLines(installed), significantLines(tmpl)):
			d.State = FragmentStale
		}
		if d.State != FragmentCurrent {
			d.Missing, d.Extra = lineDelta(installed, tmpl)
			// A variant with no key-level delta already carries everything the
			// catalog declares; its remaining differences ARE the variant.
			if d.State == FragmentVariant && len(d.Missing) == 0 && len(d.Extra) == 0 {
				d.State = FragmentCurrent
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// catalogTemplate resolves an installed fragment name to its catalog template.
// `teled` matches `teled.yml`; the multi-account variant `teled-rhias` falls
// back to the base kind's template, the same `<kind>-<label>` split envFileName
// uses to share one env file across accounts.
func catalogTemplate(tmplDir, name string) (kind string, body []byte, ok bool) {
	for _, k := range candidateKinds(name) {
		b, err := os.ReadFile(filepath.Join(tmplDir, k+".yml"))
		if err == nil {
			return k, b, true
		}
	}
	return "", nil, false
}

func candidateKinds(name string) []string {
	if base, _, found := strings.Cut(name, "-"); found {
		return []string{name, base}
	}
	return []string{name}
}

// lineDelta reports the catalog lines whose field is absent from the installed
// copy (missing) and the installed lines whose field the catalog no longer
// declares (extra) — "the state mount is gone" and "CHANNEL_SECRET is still
// here", which is what the operator acts on.
func lineDelta(installed, tmpl []byte) (missing, extra []string) {
	have, want := significantLines(installed), significantLines(tmpl)
	return absentFrom(want, have), absentFrom(have, want)
}

// absentFrom returns the lines of a whose field key — or, for a keyless list
// item, whose exact text — does not occur in b.
func absentFrom(a, b []string) []string {
	keys, lines := map[string]bool{}, map[string]bool{}
	for _, l := range b {
		lines[strings.TrimSpace(l)] = true
		if k := fieldKey(l); k != "" {
			keys[k] = true
		}
	}
	var out []string
	for _, l := range a {
		if k := fieldKey(l); k != "" {
			if !keys[k] {
				out = append(out, l)
			}
			continue
		}
		if !lines[strings.TrimSpace(l)] {
			out = append(out, l)
		}
	}
	return out
}

// fieldKey returns the YAML key a line sets, empty for a list item or a bare
// value. The report compares key PRESENCE, not value: a multi-account variant
// legitimately holds its own container_name and LISTEN_URL, and printing those
// as drift on every generate would bury the lines that matter.
func fieldKey(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "- ") {
		return ""
	}
	k, _, ok := strings.Cut(t, ":")
	if !ok {
		return ""
	}
	return k
}

// significantLines drops blanks and comments: a reworded header comment is not
// a change an operator needs to act on.
func significantLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return out
}

// Report renders the drift as operator-facing lines, one block per fragment
// that needs attention. Empty when every fragment is current or local — silence
// is the "nothing to do" signal on a clean deploy.
func Report(plan []FragmentDrift) []string {
	var out []string
	for _, d := range plan {
		switch {
		case d.State == FragmentStale && len(d.Missing)+len(d.Extra) == 0:
			// Same fields, different values (an image tag, a URL) — sync still
			// applies it, there is just no field-level line to show.
			out = append(out, fmt.Sprintf("services/%s is behind the bundled %s package (values differ)", d.File, d.Kind))
			continue
		case d.State == FragmentStale:
			out = append(out, fmt.Sprintf("services/%s is behind the bundled %s package:", d.File, d.Kind))
		case d.State == FragmentVariant:
			out = append(out, fmt.Sprintf("services/%s is a local variant of %s; apply by hand:", d.File, d.Kind))
		default:
			continue
		}
		for _, l := range d.Missing {
			out = append(out, "  + "+strings.TrimSpace(l))
		}
		for _, l := range d.Extra {
			out = append(out, "  - "+strings.TrimSpace(l))
		}
	}
	return out
}
