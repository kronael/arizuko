package store

import "testing"

// TestPutDeleteProxydRoute is P2's hot-apply core (spec 5/28): the CLI writes a
// package's proxyd route straight into the live table (proxyd reads it per
// request), and removal deletes it. Upsert is delete-then-insert.
func TestPutDeleteProxydRoute(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := ProxydRoute{Path: "/slack/", Backend: "http://slakd:8080", Auth: "public",
		GatedBy: "SLACK_BOT_TOKEN", PreserveHeaders: []string{"X-Slack-Signature"}}
	if err := s.PutProxydRoute(r); err != nil {
		t.Fatalf("put: %v", err)
	}
	// upsert (change backend), no duplicate row
	r.Backend = "http://slakd2:8080"
	if err := s.PutProxydRoute(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, err := s.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range all {
		if x.Path == "/slack/" {
			n++
			if x.Backend != "http://slakd2:8080" || len(x.PreserveHeaders) != 1 {
				t.Fatalf("row not upserted: %+v", x)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 /slack/ row, got %d", n)
	}
	ok, err := s.DeleteProxydRoute("/slack/")
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.DeleteProxydRoute("/slack/"); ok {
		t.Fatal("second delete reported ok")
	}
}
