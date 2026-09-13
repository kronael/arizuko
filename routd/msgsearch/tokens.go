package msgsearch

// tokens.go is the two places this package reads TEXT rather than structure:
// the tokeniser that decides what a word is, and the snippet that reports one
// back to the caller.
//
// A TERM IS A RUN OF UNICODE LETTERS AND DIGITS, LOWER-CASED, WITH DIACRITICS
// DROPPED. The folding is what makes an accented language searchable from a
// plain keyboard: "uroven" has to answer "úroveň", or every Czech, French and
// Vietnamese message is reachable only by someone who can type its accents.
// Dropping the marks is one rule for every script, so no language carries a
// table of its own.
//
// THE OFFSETS ARE THE POINT OF A TOKEN. A term is folded, so it cannot be shown
// to anyone; the snippet marks the hit inside the bytes the writer wrote,
// capitals and accents and all, and it can only do that because every token
// carries where it came from.

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// token is one word of a message: the term it is indexed and searched under,
// and the bytes of the original it was folded from.
type token struct {
	term  string
	start int
	end   int
}

// tokenRune reports whether r belongs inside a token.
//
// COMBINING MARKS COUNT. Text arrives in whatever normal form its sender's
// keyboard produced, and in NFD the accent of "úroveň" is a rune of its own
// (category Mn) sitting between two letters. A boundary there would index four
// terms for one word, and the same word typed in NFC would index one — the two
// spellings of one message would answer different searches.
func tokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// tokenize splits text into its tokens, in order.
func tokenize(text string) []token {
	var out []token
	start := -1
	for i, r := range text {
		if tokenRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = appendToken(out, text, start, i)
			start = -1
		}
	}
	if start >= 0 {
		out = appendToken(out, text, start, len(text))
	}
	return out
}

// appendToken folds one run and keeps it if anything is left. A run of nothing
// but combining marks folds to the empty string, and an empty term would be a
// word every document holds.
func appendToken(out []token, text string, start, end int) []token {
	term := fold(text[start:end])
	if term == "" {
		return out
	}
	return append(out, token{term: term, start: start, end: end})
}

// terms is the tokeniser's answer for a caller that wants only the words —
// the query parser, which has no text to mark.
func terms(text string) []string {
	toks := tokenize(text)
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.term
	}
	return out
}

// fold renders one run as the term it is stored under: NFD decomposition, the
// combining marks dropped, the rest lower-cased.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// The snippet's marks and its width. The callers render the string as it
// arrives — a chat message, a dashboard row — so the marks are the ones they
// already display around a hit.
const (
	markOpen      = "«"
	markClose     = "»"
	ellipsis      = "…"
	snippetTokens = 32
)

// snippet reports one hit: a window of at most snippetTokens tokens around the
// first match, every matched term inside it wrapped, and an ellipsis wherever
// the window cuts the message.
//
// The window is centred on the first hit rather than taken from the head of the
// message, because a hit 200 words into a turn's output would otherwise be
// reported with 32 words that do not contain it — a snippet with no highlight
// reads as a false positive.
func snippet(content string, toks []token, want map[string]bool) string {
	if len(toks) == 0 {
		return content
	}
	first := 0
	for i, t := range toks {
		if want[t.term] {
			first = i
			break
		}
	}
	start := first - snippetTokens/2
	if start < 0 {
		start = 0
	}
	end := start + snippetTokens
	if end > len(toks) {
		end = len(toks)
		if start = end - snippetTokens; start < 0 {
			start = 0
		}
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString(ellipsis)
	}
	// The window runs from its first token to its last, so the punctuation
	// hanging off either edge is not reported as part of the message.
	at := toks[start].start
	for i := start; i < end; i++ {
		t := toks[i]
		if !want[t.term] {
			continue
		}
		b.WriteString(content[at:t.start])
		b.WriteString(markOpen)
		b.WriteString(content[t.start:t.end])
		b.WriteString(markClose)
		at = t.end
	}
	b.WriteString(content[at:toks[end-1].end])
	if end < len(toks) {
		b.WriteString(ellipsis)
	}
	return b.String()
}
