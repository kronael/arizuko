package routd

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestConcurrentMessagesOneChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const count = 100
	const jid = "web:burst"
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- db.PutMessage(core.Message{
				ID: fmt.Sprint(i), ChatJID: jid, Sender: "user",
				Timestamp: time.Now(), Content: "message",
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != count {
		t.Fatalf("got %d messages, want %d", len(msgs), count)
	}
	ids := make(map[string]bool, count)
	for _, msg := range msgs {
		ids[msg.ID] = true
	}
	for i := range count {
		if !ids[fmt.Sprint(i)] {
			t.Errorf("missing message %d", i)
		}
	}
}
