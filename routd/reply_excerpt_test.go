package routd

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// TestReplyExcerptReadsBack: reply_to_text / reply_to_sender were written by
// PutMessage and never read by msgReadCols, so every <reply-to> rendered
// sender="unknown" with no body — 2520 of 2520 in the live transcripts of
// three instances on 2026-09-05, including the 80 rows teled had filled.
func TestReplyExcerptReadsBack(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutMessage(core.Message{
		ID: "m1", ChatJID: "telegram:user/1", Sender: "telegram:user/1", Content: "fine.",
		Timestamp: time.Now().UTC(), ReplyToID: "p9", ReplyToText: "how are you?", ReplyToSender: "Bot",
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.MessagesSince("telegram:user/1", "")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
	if msgs[0].ReplyToText != "how are you?" || msgs[0].ReplyToSender != "Bot" {
		t.Fatalf("excerpt not read back: %+v", msgs[0])
	}
}

// TestIngestFillsReplyExcerptFromStoredParent: only teled sends reply_to_text;
// on sloth 1966 of 2069 replies named a parent routd already stored. Ingest
// fills sender + a bounded excerpt from that row, keeps an adapter-supplied
// excerpt, and still promotes a reply to a bot row to verb=mention.
func TestIngestFillsReplyExcerptFromStoredParent(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Match: "platform=discord", Target: "demo"}}})
	const jid = "discord:g/c1"
	now := time.Now().UTC()
	long := strings.Repeat("x", replyExcerptChars) + "TAIL"
	_ = db.PutMessage(core.Message{ID: "human", ChatJID: jid, Sender: "discord:user/1", Name: "Alice",
		Content: long, Timestamp: now})
	_ = db.PutMessage(core.Message{ID: "mcp-1", PlatformID: "bot-pid", ChatJID: jid, Sender: "demo",
		Content: "answer", Timestamp: now, BotMsg: true})

	stored := func(id string) (text, sender, verb string) {
		t.Helper()
		m := msgByID(t, db, jid, id)
		return m.ReplyToText, m.ReplyToSender, m.Verb
	}
	post := func(id, replyTo, text string) {
		t.Helper()
		in := apiv1.Message{ID: id, ChatJID: jid, Sender: "discord:user/2", Content: "reply",
			Verb: "message", ReplyTo: replyTo, ReplyToText: text}
		if rec := doJSON(t, h, "POST", "/v1/messages", "", in); rec.Code != 200 {
			t.Fatalf("ingest %s=%d %s", id, rec.Code, rec.Body.String())
		}
	}

	post("r1", "human", "")
	if text, sender, verb := stored("r1"); text != long[:replyExcerptChars] || sender != "Alice" || verb != "message" {
		t.Fatalf("human parent: text=%q sender=%q verb=%q", text, sender, verb)
	}
	post("r2", "bot-pid", "")
	if text, sender, verb := stored("r2"); text != "answer" || sender != "demo" || verb != "mention" {
		t.Fatalf("bot parent by platform_id: text=%q sender=%q verb=%q", text, sender, verb)
	}
	post("r3", "human", "adapter excerpt")
	if text, sender, _ := stored("r3"); text != "adapter excerpt" || sender != "" {
		t.Fatalf("adapter excerpt overwritten: text=%q sender=%q", text, sender)
	}
	post("r4", "never-stored", "")
	if text, sender, _ := stored("r4"); text != "" || sender != "" {
		t.Fatalf("unknown parent invented an excerpt: text=%q sender=%q", text, sender)
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	got := truncate("aé", 2)
	if got != "a" || !utf8.ValidString(got) {
		t.Fatalf("truncate cut inside a rune: %q", got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Fatalf("truncate shortened a fitting string: %q", got)
	}
}

// Bare and chat-prefixed bot references inherit the excerpt and topic as mentions.
func TestPrefixedReferenceResolvesABotRow(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "nemo"})
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{
		"routes": []apiv1.Route{{Match: "platform=telegram", Target: "nemo"}}})
	const jid = "telegram:group/5229089286"

	_ = db.PutMessage(core.Message{ID: "out-abc", PlatformID: "4242", ChatJID: jid,
		Sender: "nemo", Content: "the bot said this", BotMsg: true,
		Timestamp: time.Now().UTC(), Topic: "bot-topic"})

	for _, verb := range []string{"message", "like"} {
		for _, ref := range []string{"4242", jid + "/4242"} {
			in := apiv1.Message{
				ID: verb + "-" + ref, ChatJID: jid, Sender: "telegram:user/1112184352",
				Content: "👍", Verb: verb, ReplyTo: ref,
			}
			if rec := doJSON(t, h, "POST", "/v1/messages", "", in); rec.Code != 200 {
				t.Fatalf("ingest %q = %d %s", ref, rec.Code, rec.Body.String())
			}
			m := msgByID(t, db, jid, verb+"-"+ref)
			if m.ReplyToText != "the bot said this" {
				t.Errorf("reference %q: excerpt = %q, want the bot's own text", ref, m.ReplyToText)
			}
			if m.Topic != "bot-topic" || m.ReplyToSender != "nemo" {
				t.Errorf("reference %q: topic=%q sender=%q", ref, m.Topic, m.ReplyToSender)
			}
			if m.Verb != "mention" {
				t.Errorf("reference %q: verb = %q, want mention — a reaction on a bot "+
					"message is addressed to the bot", ref, m.Verb)
			}
		}
	}
	if _, ok := db.MessageByID("telegram:group/other", jid+"/4242"); ok {
		t.Fatal("reference resolved a parent from another chat")
	}
	if _, ok := db.MessageByID(jid, "telegram:group/other/4242"); ok {
		t.Fatal("foreign chat prefix resolved the local platform id")
	}
	if err := db.PutMessage(core.Message{ID: jid + "/4242", ChatJID: jid,
		Content: "own id wins", Timestamp: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if m, ok := db.MessageByID(jid, jid+"/4242"); !ok || m.Content != "own id wins" {
		t.Fatalf("own id did not take precedence: %+v, found=%v", m, ok)
	}
}
