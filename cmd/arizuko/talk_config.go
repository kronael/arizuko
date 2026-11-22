package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type talkEntry struct {
	URL   string
	Token string
	Group string
}

func (e talkEntry) complete() bool { return e.URL != "" && e.Token != "" }

func talkConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "arizuko", "talk.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "arizuko", "talk.toml")
	}
	return filepath.Join(home, ".config", "arizuko", "talk.toml")
}

func parseTalkTOML(data []byte) (talkEntry, map[string]talkEntry, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return talkEntry{}, nil, err
	}
	named := map[string]talkEntry{}
	for k, v := range raw {
		if sect, ok := v.(map[string]any); ok {
			e, err := entryFromMap(sect, "["+k+"]")
			if err != nil {
				return talkEntry{}, nil, err
			}
			named[k] = e
			delete(raw, k)
		}
	}
	top, err := entryFromMap(raw, "")
	return top, named, err
}

func entryFromMap(m map[string]any, where string) (talkEntry, error) {
	var e talkEntry
	if where != "" {
		where += ": "
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return e, fmt.Errorf("%skey %q must be a string", where, k)
		}
		switch k {
		case "url":
			e.URL = s
		case "token":
			e.Token = s
		case "group":
			e.Group = s
		default:
			return e, fmt.Errorf("%sunknown key %q (want url, token, group)", where, k)
		}
	}
	return e, nil
}

func loadTalkConfig(path string) (top talkEntry, named map[string]talkEntry, found bool, err error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return talkEntry{}, nil, false, nil
	}
	if err != nil {
		return talkEntry{}, nil, false, err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return talkEntry{}, nil, false, fmt.Errorf(
			"%s is mode %04o and holds a bearer token; run: chmod 600 %s", path, perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return talkEntry{}, nil, false, err
	}
	top, named, err = parseTalkTOML(data)
	if err != nil {
		return talkEntry{}, nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return top, named, true, nil
}

func resolveTalkEntry(f talkFlags, top talkEntry, named map[string]talkEntry, cfgFound bool, getenv func(string) string, cfgPath string) (talkEntry, error) {
	var e talkEntry
	switch {
	case f.Section != "":
		sect, ok := named[f.Section]
		if !ok {
			if !cfgFound {
				return talkEntry{}, fmt.Errorf("no config file, so @%s names nothing\n\n%s", f.Section, talkConfigHelp(cfgPath))
			}
			return talkEntry{}, fmt.Errorf("%s has no [%s] section (have: %s)",
				cfgPath, f.Section, sectionList(named))
		}
		e = sect
	case top.URL != "" || top.Token != "":
		e = top
	default:
		e = talkEntry{
			URL:   getenv("ARIZUKO_CHAT_URL"),
			Token: getenv("ARIZUKO_CHAT_TOKEN"),
			Group: getenv("ARIZUKO_CHAT_GROUP"),
		}
	}
	if f.URL != "" {
		e.URL = f.URL
	}
	if f.Token != "" {
		e.Token = f.Token
	}
	if f.Group != "" {
		e.Group = f.Group
	}
	e.URL = strings.TrimRight(strings.TrimSpace(e.URL), "/")
	if !e.complete() {
		return talkEntry{}, fmt.Errorf("no chat configured\n\n%s", talkConfigHelp(cfgPath))
	}
	return e, nil
}

func sectionList(named map[string]talkEntry) string {
	if len(named) == 0 {
		return "none"
	}
	out := make([]string, 0, len(named))
	for k := range named {
		out = append(out, "@"+k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func talkConfigHelp(path string) string {
	return fmt.Sprintf(`Write %s (mode 0600):

    url   = "https://your-instance.example"
    token = "<bearer>"
    # group = "solo/inbox"   # optional; omit when you can reach exactly one

Then:  chmod 600 %s

On the instance host, grant the subject and mint its bearer:

    arizuko group [-i instance] grant cli '<folder>/**'
    arizuko token [-i instance] issue bearer <folder> \
        --sub user:cli --scope messages:read,messages:write --ttl 720h

The bearer's scope claim is not read on this path: proxyd re-reads the
subject's acl grants, so the `+"`grant`"+` above is what decides reach.

Or set ARIZUKO_CHAT_URL + ARIZUKO_CHAT_TOKEN in the environment.`, path, path)
}
