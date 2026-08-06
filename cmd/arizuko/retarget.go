package main

// `arizuko apply --as-folder <folder>` — spec 5/8 §"Path retargeting:
// primitive shipped, no caller". The engine-owned primitive is
// resreg.Resource.Retarget; this is the recipe over the two existing verbs
// (export somewhere, apply here) the spec asks for, NOT a new copy-folder
// mechanism. Its named consumers are 5/28's seed-once package group and
// cross-instance folder migration.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// retargetDocs rewrites every folder-scoped row in docs to newFolder, in
// place, and returns the folder they came from.
//
// It REFUSES a manifest whose scoped rows do not all name one single folder,
// rather than guessing which of several the operator meant — the same
// discipline Retarget itself applies to columns it cannot decide. The empty
// scope counts as a distinct folder here on purpose: network_rules carries
// folder='' for instance-global rules, and silently retargeting those would
// convert an instance-wide egress allowlist into one folder's, which is a
// privilege change, not a rename.
func retargetDocs(docs []parsedDoc, newFolder string) (string, error) {
	if newFolder == "" {
		return "", fmt.Errorf("--as-folder needs a folder")
	}
	seen := map[string]bool{}
	for _, d := range docs {
		for _, s := range resreg.ManifestScopes(d.subsystem, d.manifest) {
			seen[s] = true
		}
	}
	switch len(seen) {
	case 0:
		return "", fmt.Errorf("--as-folder: this manifest has no folder-scoped rows to retarget")
	case 1:
	default:
		var all []string
		for s := range seen {
			all = append(all, fmt.Sprintf("%q", s))
		}
		return "", fmt.Errorf("--as-folder needs a single-folder manifest; this one names %s. "+
			"Trim it to one folder — retargeting several onto one would merge them silently",
			strings.Join(all, ", "))
	}
	var src string
	for s := range seen {
		src = s
	}
	if src == newFolder {
		return "", fmt.Errorf("--as-folder %s: the manifest already targets that folder", newFolder)
	}

	for _, d := range docs {
		for _, r := range resreg.BySubsystem(d.subsystem) {
			rows, mentioned := d.manifest[r.Name]
			if !mentioned || !r.HasScope() {
				continue
			}
			out, err := r.Retarget(rows, newFolder)
			if err != nil {
				return "", err
			}
			d.manifest[r.Name] = out
		}
		retargetRedirects(d.manifest, src, newFolder)
	}
	return src, nil
}

// retargetRedirects closes the gap the spec names: Retarget rewrites ONLY the
// declared ScopeSpec.Field column, but web_routes.redirect_to also embeds the
// folder — it points into that folder's own /pub|priv/<folder>/ web root — so
// a retargeted route would keep serving the SOURCE folder's files. Rewriting
// it belongs to the caller, and this is the caller.
func retargetRedirects(manifest map[string]any, src, dst string) {
	rows, ok := manifest["web_routes"].([]resources.WebRoutesRow)
	if !ok {
		return
	}
	for i := range rows {
		rows[i].RedirectTo = strings.ReplaceAll(rows[i].RedirectTo, "/"+src+"/", "/"+dst+"/")
	}
}
