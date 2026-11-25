// Package msgsearch is message search: a bounded newest-first scan of the
// store that matches each row it reads and holds nothing between two calls
// (specs/6/42-dynamo-over-postgres.md).
package msgsearch

import (
	"context"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

const (
	DefaultLimit = 20
	MaxLimit     = 200
	// Budget is how many stored rows one search reads before it answers how
	// far it got.
	Budget = 20000
)

// Pages is a scope's messages, newest first. Next answers no rows when the
// scope is exhausted, and a page never ends inside an instant. Read is how many
// rows the store has returned so far.
type Pages interface {
	Next(ctx context.Context) ([]store.ScannedMessage, error)
	Read() int
}

// Search reads pages until it holds limit hits, reaches since, exhausts the
// scope or spends Budget. It stops only between two instants, so a caller that
// continues with before = SearchedTo reads each message one time.
func Search(ctx context.Context, pages Pages, query, sender string, since time.Time,
	limit int) (apiv1.FindResponse, error) {
	items, err := parseQuery(query)
	if err != nil {
		return apiv1.FindResponse{}, err
	}
	if limit <= 0 || limit > MaxLimit {
		limit = DefaultLimit
	}
	want := wanted(items)
	hits := []apiv1.FoundMessage{}
	var last time.Time
	answer := func(complete bool) apiv1.FindResponse {
		out := apiv1.FindResponse{Messages: hits, Count: len(hits), Complete: complete}
		if !last.IsZero() {
			out.SearchedTo = last.UTC().Format(time.RFC3339Nano)
		}
		return out
	}
	for {
		rows, err := pages.Next(ctx)
		if err != nil {
			return apiv1.FindResponse{}, err
		}
		if len(rows) == 0 {
			return answer(true), nil
		}
		for _, row := range rows {
			if len(hits) >= limit && !row.Timestamp.Equal(last) {
				return answer(false), nil
			}
			if row.Timestamp.Before(since) {
				return answer(true), nil
			}
			last = row.Timestamp
			if sender != "" && row.Sender != sender {
				continue
			}
			toks := tokenize(row.Content)
			if !isMatch(items, toks) {
				continue
			}
			hits = append(hits, apiv1.FoundMessage{
				ChatJID:      row.ChatJID,
				Sender:       row.Sender,
				Timestamp:    row.Timestamp.UTC().Format(time.RFC3339Nano),
				IsFromMe:     row.FromMe,
				IsBotMessage: row.BotMsg,
				Content:      snippet(row.Content, toks, want),
			})
		}
		if len(hits) >= limit || pages.Read() >= Budget {
			return answer(false), nil
		}
	}
}

func isMatch(items []item, toks []token) bool {
	for _, it := range items {
		if hasPhrase(toks, it.terms) == it.not {
			return false
		}
	}
	return true
}

func hasPhrase(toks []token, phrase []string) bool {
	for i := 0; i+len(phrase) <= len(toks); i++ {
		hit := true
		for j, term := range phrase {
			if toks[i+j].term != term {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}
