package main

import (
	"flag"
	"fmt"
	"strings"
)

// flexParse lets flags appear anywhere in args — before, between, or after
// positionals — unlike std flag, which stops at the first non-flag arg. It
// reorders args so all flags come first, then delegates the actual parsing to
// fs.Parse (we only reorder; std flag does the work). After it returns,
// positionals are available via fs.Args().
//
// Detection rules:
//   - "--" terminates flag detection; everything after is positional.
//   - A token is a flag if it starts with "-", is longer than 1 char, and is
//     not a bare "-".
//   - A value-flag with no inline "=value" consumes the NEXT token as its
//     value (so `-seq -10` keeps -10 as the value, not a flag).
func flexParse(fs *flag.FlagSet, args []string) error {
	var flagsList, posList []string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			posList = append(posList, args[i+1:]...)
			break
		}
		if !isFlagToken(tok) {
			posList = append(posList, tok)
			continue
		}
		name, hasInline := flagName(tok)
		f := fs.Lookup(name)
		if f == nil {
			return fmt.Errorf("flag provided but not defined: -%s", name)
		}
		if hasInline || isBoolFlag(f) {
			// Self-contained: "-x=v", "-x" (bool). Std flag parses it alone.
			flagsList = append(flagsList, tok)
			continue
		}
		// Value flag with no inline value: it needs the next token as its
		// value. Pull it in regardless of whether it looks like a flag (this
		// is what keeps a negative number like -10 as the value).
		flagsList = append(flagsList, tok)
		if i+1 < len(args) {
			flagsList = append(flagsList, args[i+1])
			i++
		}
	}
	return fs.Parse(append(flagsList, posList...))
}

// isFlagToken reports whether tok looks like a flag: starts with "-", longer
// than one char, and not a bare "-".
func isFlagToken(tok string) bool {
	return len(tok) > 1 && tok[0] == '-'
}

// flagName strips leading dashes and returns the flag name up to the first "="
// plus whether an inline "=value" was present.
func flagName(tok string) (name string, hasInline bool) {
	s := strings.TrimLeft(tok, "-")
	if eq := strings.IndexByte(s, '='); eq >= 0 {
		return s[:eq], true
	}
	return s, false
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
