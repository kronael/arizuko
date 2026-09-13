package msgsearch

// query.go is the query grammar: terms, "phrases" and `-` for negation.
//
// THE GRAMMAR IS THE WHOLE GRAMMAR. Terms are AND-ed, because a search for two
// words means both words to everyone who has used one; a phrase matches its
// terms adjacent and in order; a `-` in front of either excludes it. There is no
// OR, no prefix wildcard and no proximity operator, and the tool description
// says so — a description that promises an operator the engine does not run
// hands the agent a silent empty result and it reports "no such message".
//
// A MALFORMED QUERY IS THE CALLER'S AND IT REACHES THE CALLER. An unclosed
// phrase is an error, not an empty hit list: the agent reads an empty list as
// fact and says the message does not exist.

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// item is one clause of a parsed query: a single term, or a phrase's terms in
// the order they must appear. `not` excludes what it matches.
type item struct {
	terms []string
	not   bool
}

// errDanglingNegation is a `-` with nothing to exclude.
var errDanglingNegation = errors.New(
	`a "-" excludes the term or phrase written straight after it, and this one has nothing after it`)

// parseQuery reads a query into its clauses.
func parseQuery(q string) ([]item, error) {
	var items []item
	neg := false
	for i := 0; i < len(q); {
		r, size := utf8.DecodeRuneInString(q[i:])
		switch {
		case unicode.IsSpace(r):
			if neg {
				return nil, errDanglingNegation
			}
			i += size
		case r == '-' && !neg:
			neg = true
			i += size
		case r == '"':
			rest := strings.IndexByte(q[i+1:], '"')
			if rest < 0 {
				return nil, fmt.Errorf("the phrase %q is never closed: a phrase is written "+
					"between two double quotes", q[i:])
			}
			phrase := terms(q[i+1 : i+1+rest])
			if len(phrase) == 0 {
				return nil, fmt.Errorf("the phrase %q holds no word to match", q[i:i+rest+2])
			}
			items = append(items, item{terms: phrase, not: neg})
			neg = false
			i += rest + 2
		default:
			word := i
			for word < len(q) {
				r2, s2 := utf8.DecodeRuneInString(q[word:])
				if unicode.IsSpace(r2) || r2 == '"' {
					break
				}
				word += s2
			}
			// One run can hold several terms — "co-op" is two words to the
			// tokeniser — and each is its own clause, so both are required.
			for _, t := range terms(q[i:word]) {
				items = append(items, item{terms: []string{t}, not: neg})
			}
			neg = false
			i = word
		}
	}
	if neg {
		return nil, errDanglingNegation
	}
	positive := 0
	for _, it := range items {
		if !it.not {
			positive++
		}
	}
	if positive == 0 {
		return nil, fmt.Errorf("%q holds no word to search for; a search that only excludes "+
			"has nothing to rank", q)
	}
	return items, nil
}

// wanted is the set of terms a hit's snippet marks: every term the query asked
// FOR. An excluded term cannot appear in a hit, so it is not in the set.
func wanted(items []item) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		if it.not {
			continue
		}
		for _, t := range it.terms {
			out[t] = true
		}
	}
	return out
}
