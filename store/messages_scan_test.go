package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

var scanEpoch = time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)

// scanSeed writes one message at an exact instant, so two messages can share
// one: the delivery and onboarding writers do the same.
func scanSeed(t *testing.T, s *Store, chat, folder, id string, minute int) {
	t.Helper()
	m := core.Message{
		ID: id, ChatJID: chat, Sender: "u", Content: "body " + id, RoutedTo: folder,
		Timestamp: scanEpoch.Add(time.Duration(minute) * time.Minute),
	}
	if _, err := s.putMessage(t.Context(), m, core.MessageStatusSent,
		messageOptions{exact: true}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

type scanner interface {
	Next(ctx context.Context) ([]ScannedMessage, error)
}

// drained reads a scan to its end and answers the ids of each page.
func drained(t *testing.T, scan scanner) [][]string {
	t.Helper()
	var pages [][]string
	for {
		rows, err := scan.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if len(rows) == 0 {
			return pages
		}
		var ids []string
		for _, row := range rows {
			_, id, _ := MsgCoord(row.Sort)
			if row.Content != "body "+id {
				t.Errorf("row %s carries content %q", id, row.Content)
			}
			ids = append(ids, id)
		}
		pages = append(pages, ids)
	}
}

func flat(pages [][]string) string { return strings.Join(slices.Concat(pages...), " ") }

// A chat is read newest first across page boundaries, each message one time.
func TestChatScanReadsNewestFirstAcrossPages(t *testing.T) {
	s := openMem(t)
	for i, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		scanSeed(t, s, "tg:1", "", id, i)
	}
	scanSeed(t, s, "tg:2", "", "other", 3)
	scan := s.ScanChat("tg:1", time.Time{})
	scan.page = 3
	pages := drained(t, scan)
	if got := flat(pages); got != "g f e d c b a" {
		t.Errorf("scan read %q, want g f e d c b a", got)
	}
	if len(pages) < 3 {
		t.Errorf("7 rows at 3 a read came back in %d pages; the page limit is not applied", len(pages))
	}
	if scan.Read() < 7 {
		t.Errorf("Read = %d after 7 rows", scan.Read())
	}
}

// A search stops only between two instants, so a page that ended inside one
// would make the continuation skip or repeat its siblings.
func TestChatScanNeverEndsAPageInsideAnInstant(t *testing.T) {
	s := openMem(t)
	for _, row := range []struct {
		id     string
		minute int
	}{{"a", 1}, {"b", 2}, {"c1", 3}, {"c2", 3}, {"c3", 3}, {"c4", 3}, {"d", 4}, {"e", 5}} {
		scanSeed(t, s, "tg:1", "", row.id, row.minute)
	}
	scan := s.ScanChat("tg:1", time.Time{})
	scan.page = 3
	pages := drained(t, scan)
	if got := flat(pages); got != "e d c4 c3 c2 c1 b a" {
		t.Fatalf("scan read %q, want each message one time, newest first", got)
	}
	for i, page := range pages {
		holds := 0
		for _, id := range page {
			if strings.HasPrefix(id, "c") {
				holds++
			}
		}
		if holds != 0 && holds != 4 {
			t.Errorf("page %d holds %d of the 4 messages of one instant: %v", i, holds, page)
		}
	}
}

// `before` is exclusive: a continuation from an instant reads nothing at it.
func TestChatScanStartsStrictlyBefore(t *testing.T) {
	s := openMem(t)
	for i, id := range []string{"a", "b", "c", "d"} {
		scanSeed(t, s, "tg:1", "", id, i)
	}
	scan := s.ScanChat("tg:1", scanEpoch.Add(2*time.Minute))
	if got := flat(drained(t, scan)); got != "b a" {
		t.Errorf("scan before minute 2 read %q, want b a", got)
	}
}

// A folder scope is the folder and everything below it, merged newest first.
// `worldly` shares a prefix with `world` and is not below it; an unrouted
// message is in no folder.
func TestFolderScanMergesTheSubtreeNewestFirst(t *testing.T) {
	s := openMem(t)
	scanSeed(t, s, "tg:a", "world", "a1", 1)
	scanSeed(t, s, "tg:b", "world/eng", "b1", 2)
	scanSeed(t, s, "tg:a", "world", "a2", 3)
	scanSeed(t, s, "tg:c", "worldly", "c1", 4)
	scanSeed(t, s, "tg:d", "", "d1", 5)
	scanSeed(t, s, "tg:b", "world/eng", "b2", 6)
	scanSeed(t, s, "tg:e", "world/eng/sre", "e1", 7)
	scan, err := s.ScanFolders(t.Context(), "world", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	scan.page = 2
	pages := drained(t, scan)
	if got := flat(pages); got != "e1 b2 a2 b1 a1" {
		t.Errorf("scan of world read %q, want e1 b2 a2 b1 a1", got)
	}
	if len(pages) < 2 {
		t.Errorf("5 rows at 2 a read came back in %d pages", len(pages))
	}

	every, err := s.ScanFolders(t.Context(), "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := flat(drained(t, every)); got != "e1 b2 c1 a2 b1 a1" {
		t.Errorf("scan of every folder read %q, want e1 b2 c1 a2 b1 a1", got)
	}

	older, err := s.ScanFolders(t.Context(), "world", scanEpoch.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := flat(drained(t, older)); got != "b1 a1" {
		t.Errorf("scan of world before minute 3 read %q, want b1 a1", got)
	}
}

// One chat can feed two folders. The scan returns the rows its folder points
// at and counts the rows it read between them.
func TestFolderScanReturnsOnlyThePointedRowsOfASharedChat(t *testing.T) {
	s := openMem(t)
	for i, id := range []string{"w1", "z1", "z2", "w2", "z3", "w3"} {
		folder := "world"
		if strings.HasPrefix(id, "z") {
			folder = "zoo"
		}
		scanSeed(t, s, "tg:shared", folder, id, i)
	}
	scan, err := s.ScanFolders(t.Context(), "world", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	scan.page = 2
	if got := flat(drained(t, scan)); got != "w3 w2 w1" {
		t.Errorf("scan of world read %q, want w3 w2 w1", got)
	}
	if scan.Read() <= 3 {
		t.Errorf("Read = %d; the rows of zoo that lie between world's rows were read and "+
			"must count against the budget", scan.Read())
	}
}

// Two chats can hold a message at one instant. Both come in one page, however
// small the page is.
func TestFolderScanNeverEndsAPageInsideAnInstant(t *testing.T) {
	s := openMem(t)
	scanSeed(t, s, "tg:a", "world", "a1", 1)
	scanSeed(t, s, "tg:a", "world", "a2", 2)
	scanSeed(t, s, "tg:b", "world/eng", "b2", 2)
	scanSeed(t, s, "tg:c", "world/ops", "c2", 2)
	scanSeed(t, s, "tg:a", "world", "a3", 3)
	scan, err := s.ScanFolders(t.Context(), "world", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	scan.page = 2
	pages := drained(t, scan)
	if len(slices.Concat(pages...)) != 5 {
		t.Fatalf("scan read %v, want 5 messages one time each", pages)
	}
	for i, page := range pages {
		holds := 0
		for _, id := range page {
			if strings.HasSuffix(id, "2") {
				holds++
			}
		}
		if holds != 0 && holds != 3 {
			t.Errorf("page %d holds %d of the 3 messages of one instant: %v", i, holds, page)
		}
	}
}
