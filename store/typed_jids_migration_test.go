package store

import (
	"database/sql"
	"strings"
	"testing"
)

// TestTypedJidsMigration round-trips legacy JID formats through the
// 0042-typed-jids migration and asserts each platform's rewrite rule.
// Strategy: open an in-memory DB (which has run all migrations up to and
// including 0038, so values are already in canonical form), seed a few
// legacy rows, then run the migration SQL again — it's idempotent, with
// all UPDATE clauses guarded by NOT LIKE on the new shape.
func TestTypedJidsMigration(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Inject legacy rows directly via raw INSERTs.
	cases := []struct {
		platform        string
		insertSQL       string
		legacyChat      string
		legacySender    string
		expectedChat    string
		expectedSender  string
		isGroup         int
	}{
		// Telegram: positive ID = user DM, negative = group/supergroup.
		{
			platform:       "telegram-user",
			legacyChat:     "telegram:123456",
			legacySender:   "telegram:987654",
			expectedChat:   "telegram:user/123456",
			expectedSender: "telegram:user/987654",
			isGroup:        0,
		},
		{
			platform:       "telegram-group",
			legacyChat:     "telegram:-1001234567",
			legacySender:   "telegram:111222",
			expectedChat:   "telegram:group/1001234567",
			expectedSender: "telegram:user/111222",
			isGroup:        1,
		},
		// Mastodon: drop host, account/<id>.
		{
			platform:       "mastodon",
			legacyChat:     "mastodon:42",
			legacySender:   "mastodon:99",
			expectedChat:   "mastodon:account/42",
			expectedSender: "mastodon:account/99",
			isGroup:        0,
		},
		// Reddit: t1_/t2_/t3_ → kind discriminator.
		{
			platform:       "reddit-comment",
			legacyChat:     "reddit:t1_abc123",
			legacySender:   "reddit:t2_alice",
			expectedChat:   "reddit:comment/abc123",
			expectedSender: "reddit:user/alice",
			isGroup:        0,
		},
		{
			platform:       "reddit-submission",
			legacyChat:     "reddit:t3_xyz789",
			legacySender:   "reddit:t2_bob",
			expectedChat:   "reddit:submission/xyz789",
			expectedSender: "reddit:user/bob",
			isGroup:        0,
		},
		// Discord DM: chats.is_group=0 → discord:dm/<channel>.
		{
			platform:       "discord-dm",
			legacyChat:     "reddit_legacy:dm-channel-not-yet", // placeholder, reset below
			legacySender:   "",
			expectedChat:   "",
			expectedSender: "",
			isGroup:        0,
		},
	}
	_ = cases // unused; per-platform asserts done inline below.

	now := "2026-05-01T12:00:00Z"

	type row struct {
		id, chatJid, sender, replyToSender string
	}
	rows := []row{
		{"m-tg-u", "telegram:123456", "telegram:987654", ""},
		{"m-tg-g", "telegram:-1001234567", "telegram:111222", "telegram:333"},
		{"m-mast", "mastodon:42", "mastodon:99", ""},
		{"m-rd-c", "reddit:t1_abc123", "reddit:t2_alice", ""},
		{"m-rd-s", "reddit:t3_xyz789", "reddit:t2_bob", ""},
		{"m-dc-dm", "discord:dmchan", "discord:user1", ""},
		{"m-dc-g", "discord:guildchan", "discord:user2", ""},
		{"m-bsky", "bluesky:did:plc:xyz", "bluesky:did:plc:xyz", ""},
		{"m-li", "linkedin:urn:li:person:abc", "linkedin:urn:li:person:abc", ""},
		{"m-em", "email:thread123@host", "email:alice@example.com", ""},
		{"m-wa", "whatsapp:1234@g.us", "whatsapp:5678@s.whatsapp.net", ""},
		{"m-tw", "twitter:tweet/777", "twitter:user/handle", ""},
	}

	for _, r := range rows {
		_, err := s.db.Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, timestamp, reply_to_sender) VALUES (?,?,?,?,?,?)`,
			r.id, r.chatJid, r.sender, "x", now, nilOrStr(r.replyToSender),
		)
		if err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}

	// Seed chats with is_group flags so discord split works.
	chatRows := []struct {
		jid     string
		isGroup int
	}{
		{"discord:dmchan", 0},
		{"discord:guildchan", 1},
		{"telegram:123456", 0},
		{"telegram:-1001234567", 1},
		{"mastodon:42", 0},
		{"reddit:t1_abc123", 0},
		{"reddit:t3_xyz789", 1},
		{"bluesky:did:plc:xyz", 0},
		{"linkedin:urn:li:person:abc", 0},
	}
	for _, c := range chatRows {
		_, err := s.db.Exec(`INSERT INTO chats (jid, is_group) VALUES (?, ?)`, c.jid, c.isGroup)
		if err != nil {
			t.Fatalf("seed chat %s: %v", c.jid, err)
		}
	}

	// Seed routes match patterns. routes.jid was dropped in migration 0022.
	_, err = s.db.Exec(`INSERT INTO routes (seq, match, target) VALUES
		(0, 'chat_jid=telegram:*', 'group/folder'),
		(1, 'chat_jid=discord:*', 'discord/folder')`)
	if err != nil {
		t.Fatalf("seed routes: %v", err)
	}

	// Seed user_jids and onboarding (with prefix-only keys).
	_, err = s.db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES ('sub-1', 'telegram:987654', ?)`, now)
	if err != nil {
		t.Fatalf("seed user_jids: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:-1001234567', 'pending', ?)`, now)
	if err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	// Re-run the migration SQL (idempotent: already-typed values are
	// guarded by NOT LIKE clauses).
	if err := runMigrationFile(s.db, "migrations/0042-typed-jids.sql"); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	// Assertions — messages table.
	expectMsg := []struct {
		id, chatJid, sender string
	}{
		{"m-tg-u", "telegram:user/123456", "telegram:user/987654"},
		{"m-tg-g", "telegram:group/1001234567", "telegram:user/111222"},
		{"m-mast", "mastodon:account/42", "mastodon:account/99"},
		{"m-rd-c", "reddit:comment/abc123", "reddit:user/alice"},
		{"m-rd-s", "reddit:submission/xyz789", "reddit:user/bob"},
		{"m-dc-dm", "discord:dm/dmchan", "discord:user/user1"},
		{"m-dc-g", "discord:_/guildchan", "discord:user/user2"},
		{"m-bsky", "bluesky:user/did%3Aplc%3Axyz", "bluesky:user/did%3Aplc%3Axyz"},
		// LinkedIn URN colons preserved — they're already inside the
		// path segment, not splitting it.
		{"m-li", "linkedin:user/urn:li:person:abc", "linkedin:user/urn:li:person:abc"},
	}

	for _, e := range expectMsg {
		var got, gotS string
		if err := s.db.QueryRow(`SELECT chat_jid, sender FROM messages WHERE id=?`, e.id).Scan(&got, &gotS); err != nil {
			t.Errorf("query %s: %v", e.id, err)
			continue
		}
		if got != e.chatJid {
			t.Errorf("%s: chat_jid = %q, want %q", e.id, got, e.chatJid)
		}
		if gotS != e.sender {
			t.Errorf("%s: sender = %q, want %q", e.id, gotS, e.sender)
		}
	}

	// Email: chat_jid stays unchanged (deferred; emaid still emits legacy
	// thread shape). Sender is rewritten.
	var emChat, emSender string
	if err := s.db.QueryRow(`SELECT chat_jid, sender FROM messages WHERE id='m-em'`).Scan(&emChat, &emSender); err != nil {
		t.Errorf("email row: %v", err)
	}
	if emChat != "email:thread123@host" {
		t.Errorf("email chat_jid = %q, want unchanged email:thread123@host", emChat)
	}
	if emSender != "email:address/alice@example.com" {
		t.Errorf("email sender = %q, want email:address/alice@example.com", emSender)
	}

	// Whatsapp + twitter: already shape-compliant, must remain unchanged.
	var waChat, twChat string
	if err := s.db.QueryRow(`SELECT chat_jid FROM messages WHERE id='m-wa'`).Scan(&waChat); err != nil {
		t.Errorf("whatsapp: %v", err)
	}
	if waChat != "whatsapp:1234@g.us" {
		t.Errorf("whatsapp untouched assertion failed: got %q", waChat)
	}
	if err := s.db.QueryRow(`SELECT chat_jid FROM messages WHERE id='m-tw'`).Scan(&twChat); err != nil {
		t.Errorf("twitter: %v", err)
	}
	if twChat != "twitter:tweet/777" {
		t.Errorf("twitter untouched: got %q", twChat)
	}

	// chats: telegram and discord rewrites
	expectChats := map[string]string{
		"telegram:user/123456":         "telegram:user/123456",
		"telegram:group/1001234567":    "telegram:group/1001234567",
		"mastodon:account/42":          "mastodon:account/42",
		"discord:dm/dmchan":            "discord:dm/dmchan",
		"discord:_/guildchan":          "discord:_/guildchan",
		"bluesky:user/did%3Aplc%3Axyz": "bluesky:user/did%3Aplc%3Axyz",
	}
	for newJid := range expectChats {
		var got string
		if err := s.db.QueryRow(`SELECT jid FROM chats WHERE jid=?`, newJid).Scan(&got); err != nil {
			t.Errorf("chats expected %q, query err: %v", newJid, err)
		}
	}

	// routes.match rewrite: chat_jid=telegram:* → chat_jid=telegram:*/*
	var matchTel string
	if err := s.db.QueryRow(`SELECT match FROM routes WHERE target='group/folder'`).Scan(&matchTel); err != nil {
		t.Errorf("route match: %v", err)
	}
	if matchTel != "chat_jid=telegram:*/*" {
		t.Errorf("route match = %q, want chat_jid=telegram:*/*", matchTel)
	}
	var matchDc string
	if err := s.db.QueryRow(`SELECT match FROM routes WHERE target='discord/folder'`).Scan(&matchDc); err != nil {
		t.Errorf("route match discord: %v", err)
	}
	if matchDc != "chat_jid=discord:*/*" {
		t.Errorf("route match = %q, want chat_jid=discord:*/*", matchDc)
	}

	// onboarding: jid rewritten in lockstep with chats.jid
	var obJid string
	if err := s.db.QueryRow(`SELECT jid FROM onboarding WHERE status='pending'`).Scan(&obJid); err != nil {
		t.Errorf("onboarding query: %v", err)
	}
	if obJid != "telegram:group/1001234567" {
		t.Errorf("onboarding jid = %q, want telegram:group/1001234567", obJid)
	}

	// user_jids: rewritten
	var ujJid string
	if err := s.db.QueryRow(`SELECT jid FROM user_jids WHERE user_sub='sub-1'`).Scan(&ujJid); err != nil {
		t.Errorf("user_jids query: %v", err)
	}
	if ujJid != "telegram:user/987654" {
		t.Errorf("user_jids jid = %q, want telegram:user/987654", ujJid)
	}
}

// runMigrationFile is a test helper: load embedded migration SQL and run it.
func runMigrationFile(db *sql.DB, path string) error {
	data, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	// SQLite supports running multiple statements via a single Exec when
	// joined with ';'. Strip lines that are pure comments to avoid driver
	// quirks; modernc handles comments fine but be defensive.
	stmts := strings.Split(string(data), ";\n")
	for _, stmt := range stmts {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func nilOrStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
