package resreg

// Archive primitives — spec 5/8 "The full-instance archive". The archive is
// a superset CONTAINING the config manifest, not a bigger manifest: this
// file adds the pieces the config-manifest engine (engine.go) deliberately
// does not cover — an unbounded event log (messages), and two
// SkipApplyRebuild resources' VALUES (secrets already lives in store/, this
// file adds route_tokens/invites) — plus the archive.yaml index shape. It
// reuses Export/EmitYAML/Checksum for the config half verbatim (cmd/arizuko/
// archive.go calls those directly); nothing here forks that renderer.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// ArchiveFormatVersion is archive.yaml's own format version — a small
// integer bumped only when an archive document's shape changes, independent
// of arizuko's release tags (spec 5/8 "Cross-instance portability").
// `archive apply` refuses outright when an archive's stamped format_version
// exceeds this.
const ArchiveFormatVersion = 1

// Consistency levels an archive.yaml declares (spec 5/8 "Consistency
// levels"). live = per-subsystem read-tx snapshots, no downtime, the
// archive as a whole is a smear across wall-clock time. quiesced = the
// operator already stopped the instance before exporting; the flag only
// stamps the metadata, this package does not stop/start anything.
const (
	ConsistencyLive     = "live"
	ConsistencyQuiesced = "quiesced"
)

// ArchiveManifest is archive.yaml: the archive's top-level index. It
// declares which consistency level produced it and, per subsystem, the read
// transaction's start timestamp — never a claim of one cross-archive
// point-in-time image (spec 5/8 "Consistency levels").
type ArchiveManifest struct {
	FormatVersion int                        `yaml:"format_version"`
	Consistency   string                     `yaml:"consistency"`
	CreatedAt     string                     `yaml:"created_at"`
	Subsystems    map[string]ArchiveSnapshot `yaml:"subsystems"`
}

// ArchiveSnapshot is one subsystem's entry in archive.yaml.
type ArchiveSnapshot struct {
	SnapshotAt string `yaml:"snapshot_at"`
	Checksum   string `yaml:"checksum"`
}

// ExportSnapshot exports one subsystem's manifest-visible projection inside
// ONE explicit read transaction, closing the gap spec 5/8 "Consistency
// levels" names: Export's several ScanAll calls were each their own
// implicit-autocommit read; wrapping them in one read-only tx gives the
// whole subsystem document a single WAL MVCC snapshot instead of one per
// resource. Returns the same manifest map Export always has, plus the
// content-hash checksum (the same value Checksum computes, without a
// second Export pass) and the snapshot's start timestamp.
func ExportSnapshot(ctx context.Context, db *sql.DB, subsystem string) (manifest map[string]any, checksum string, snapshotAt string, err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", "", fmt.Errorf("begin read tx: %w", err)
	}
	defer tx.Rollback()
	snapshotAt = time.Now().UTC().Format(time.RFC3339)
	manifest, err = Export(tx, subsystem)
	if err != nil {
		return nil, "", "", err
	}
	b, err := EmitYAML(manifest)
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(b)
	checksum = "sha256:" + hex.EncodeToString(sum[:])
	return manifest, checksum, snapshotAt, nil
}

// --- messages -----------------------------------------------------------

// ArchiveMessageRow is the FULL row shape of routd.db's messages table —
// every column, unlike routd's own agent-facing read surface (msgReadCols,
// routd/reads.go) which omits reply_to_text/reply_to_sender/is_observed/
// errored because the agent never needs them. The archive is a full
// backup, not a filtered view, so it carries all of them verbatim (spec
// 5/8 "Message history": turn_id/chat_jid "carried verbatim").
type ArchiveMessageRow struct {
	ID            string `json:"id"`
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name,omitempty"`
	Content       string `json:"content"`
	Timestamp     string `json:"timestamp"`
	IsFromMe      bool   `json:"is_from_me"`
	IsBotMessage  bool   `json:"is_bot_message"`
	ForwardedFrom string `json:"forwarded_from,omitempty"`
	ReplyToID     string `json:"reply_to_id,omitempty"`
	ReplyToText   string `json:"reply_to_text,omitempty"`
	ReplyToSender string `json:"reply_to_sender,omitempty"`
	Topic         string `json:"topic,omitempty"`
	RoutedTo      string `json:"routed_to,omitempty"`
	Verb          string `json:"verb,omitempty"`
	Attachments   string `json:"attachments,omitempty"`
	Source        string `json:"source,omitempty"`
	IsObserved    bool   `json:"is_observed"`
	TurnID        string `json:"turn_id,omitempty"`
	Status        string `json:"status,omitempty"`
	PlatformID    string `json:"platform_id,omitempty"`
	ChatName      string `json:"chat_name,omitempty"`
	Errored       bool   `json:"errored"`
	LinkContext   string `json:"link_context,omitempty"`
}

// archiveMessageCols mirrors every column of routd/migrations/0001-initial-
// schema.sql's messages table plus 0006's errored and 0019's link_context —
// the full row, not routd/reads.go's msgReadCols agent-facing projection.
const archiveMessageCols = `id, chat_jid, sender, COALESCE(sender_name,''), content, timestamp,
	is_from_me, is_bot_message, COALESCE(forwarded_from,''), COALESCE(reply_to_id,''),
	COALESCE(reply_to_text,''), COALESCE(reply_to_sender,''), COALESCE(topic,''),
	COALESCE(routed_to,''), COALESCE(verb,''), COALESCE(attachments,''), COALESCE(source,''),
	is_observed, COALESCE(turn_id,''), COALESCE(status,''), COALESCE(platform_id,''),
	COALESCE(chat_name,''), errored, COALESCE(link_context,'')`

// ExportMessagesJSONL streams every messages row (one read tx — spec 5/8
// "Consistency levels") to w as JSONL (repo convention: .jl, one JSON
// object per line), ordered by rowid (insertion order) for deterministic
// output on an unchanged DB. Returns the row count.
func ExportMessagesJSONL(ctx context.Context, db *sql.DB, w io.Writer) (int, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, fmt.Errorf("begin read tx: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+archiveMessageCols+" FROM messages ORDER BY rowid")
	if err != nil {
		return 0, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		var m ArchiveMessageRow
		var fromMe, botMsg, observed, errored int
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.SenderName, &m.Content, &m.Timestamp,
			&fromMe, &botMsg, &m.ForwardedFrom, &m.ReplyToID, &m.ReplyToText, &m.ReplyToSender,
			&m.Topic, &m.RoutedTo, &m.Verb, &m.Attachments, &m.Source, &observed,
			&m.TurnID, &m.Status, &m.PlatformID, &m.ChatName, &errored, &m.LinkContext); err != nil {
			return n, fmt.Errorf("scan message: %w", err)
		}
		m.IsFromMe = fromMe != 0
		m.IsBotMessage = botMsg != 0
		m.IsObserved = observed != 0
		m.Errored = errored != 0
		if err := enc.Encode(m); err != nil {
			return n, fmt.Errorf("encode message %s: %w", m.ID, err)
		}
		n++
	}
	return n, rows.Err()
}

// defaultImportBatch bounds how many messages ride one import transaction —
// a multi-hour transfer must not hold routd.db's write lock for its whole
// duration (spec 5/8 "Message history").
const defaultImportBatch = 500

// ImportMessagesJSONL reads JSONL from r and appends every row via INSERT OR
// IGNORE keyed on id (spec 5/8 "Message history": "idempotent bulk append,
// not rebuild" — re-running the same archive, or restoring onto a target
// that already has some of the same history, is a no-op on already-present
// rows). Runs in batches of batchSize rows per tx (<=0 uses
// defaultImportBatch). Returns the number of rows read and the set of
// chat_jids touched, for the caller to derive agent_cursor from
// (DeriveAgentCursors, below — spec 5/8's Finding 4).
func ImportMessagesJSONL(ctx context.Context, db *sql.DB, r io.Reader, batchSize int) (imported int, chatJIDs map[string]bool, err error) {
	if batchSize <= 0 {
		batchSize = defaultImportBatch
	}
	chatJIDs = map[string]bool{}
	dec := json.NewDecoder(r)
	var batch []ArchiveMessageRow
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, terr := db.BeginTx(ctx, nil)
		if terr != nil {
			return fmt.Errorf("begin import tx: %w", terr)
		}
		defer tx.Rollback()
		for _, m := range batch {
			if _, ierr := tx.ExecContext(ctx, `INSERT OR IGNORE INTO messages
				(id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
				 is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
				 topic, routed_to, verb, attachments, source, is_observed, turn_id, status,
				 platform_id, chat_name, errored, link_context)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				m.ID, m.ChatJID, m.Sender, nullStr(m.SenderName), m.Content, m.Timestamp,
				b2i(m.IsFromMe), b2i(m.IsBotMessage), nullStr(m.ForwardedFrom), nullStr(m.ReplyToID),
				nullStr(m.ReplyToText), nullStr(m.ReplyToSender), m.Topic, m.RoutedTo, m.Verb,
				m.Attachments, m.Source, b2i(m.IsObserved), nullStr(m.TurnID), m.Status,
				nullStr(m.PlatformID), m.ChatName, b2i(m.Errored), nullStr(m.LinkContext),
			); ierr != nil {
				return fmt.Errorf("insert message %s: %w", m.ID, ierr)
			}
			chatJIDs[m.ChatJID] = true
		}
		if cerr := tx.Commit(); cerr != nil {
			return fmt.Errorf("commit import batch: %w", cerr)
		}
		imported += len(batch)
		batch = batch[:0]
		return nil
	}
	for {
		var m ArchiveMessageRow
		derr := dec.Decode(&m)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			return imported, chatJIDs, fmt.Errorf("decode message: %w", derr)
		}
		batch = append(batch, m)
		if len(batch) >= batchSize {
			if ferr := flush(); ferr != nil {
				return imported, chatJIDs, ferr
			}
		}
	}
	if ferr := flush(); ferr != nil {
		return imported, chatJIDs, ferr
	}
	return imported, chatJIDs, nil
}

// DeriveAgentCursors sets chats.agent_cursor = MAX(messages.timestamp) for
// every chat_jid in touched (spec 5/8 "One exception, load-bearing:
// agent_cursor" — Finding 4). Without this, restoring message history makes
// routd's poller (routd/db.go pollOnce, GetAgentCursor reading "" for a
// chat with no row) treat the whole imported history as unseen and dispatch
// a turn for every restored chat — the agent answering every historical
// message.
//
// Never moves an EXISTING cursor backward (the WHERE guard below): the
// spec's own wording derives the cursor from "the rows just written for
// that chat", which for the primary DR case (an empty target, no prior
// chats row) is exactly MAX(timestamp) over the chat. This guard extends
// that to a restore onto an already-live, populated target without
// contradicting it — a populated target's poller must not be walked
// backward to re-open messages it already consumed.
func DeriveAgentCursors(ctx context.Context, db *sql.DB, touched map[string]bool) (int, error) {
	if len(touched) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cursor tx: %w", err)
	}
	defer tx.Rollback()
	jids := make([]string, 0, len(touched))
	for j := range touched {
		jids = append(jids, j)
	}
	sort.Strings(jids)
	n := 0
	for _, jid := range jids {
		res, uerr := tx.ExecContext(ctx, `INSERT INTO chats(jid, agent_cursor)
			SELECT ?, MAX(timestamp) FROM messages WHERE chat_jid = ?
			ON CONFLICT(jid) DO UPDATE SET agent_cursor = excluded.agent_cursor
			WHERE excluded.agent_cursor > COALESCE(chats.agent_cursor, '')`,
			jid, jid)
		if uerr != nil {
			return n, fmt.Errorf("derive agent_cursor for %s: %w", jid, uerr)
		}
		if aff, _ := res.RowsAffected(); aff > 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit cursor tx: %w", err)
	}
	return n, nil
}

// --- route_tokens + invites values (Finding 3) ---------------------------

// ArchiveRouteTokenRow is one row of the archive's route_tokens document:
// RouteTokensRow's shape (resreg/resources/route_tokens.go — jid,
// owner_folder, created_at, context) plus TokenHash, the verifier the
// config manifest never carries (spec 5/8 "Secret and token values").
// Restricted to kind='route' at read time — pairing tokens (kind='pair')
// are excluded entirely, the same RowFilter the config-manifest resource
// already applies (10-minute single-use credentials, no archival value,
// reviving one actively harmful).
type ArchiveRouteTokenRow struct {
	TokenHash   string `yaml:"token_hash"`
	JID         string `yaml:"jid"`
	OwnerFolder string `yaml:"owner_folder"`
	CreatedAt   string `yaml:"created_at"`
	Context     string `yaml:"context,omitempty"`
}

// ExportRouteTokens reads every kind='route' row (jid delivery bearers —
// never kind='pair' pairing links) with its token_hash verifier, hex-encoded
// (Go's encoding/hex, lowercase — matching invites' Ref = hex(sha256(token))
// convention) for YAML transport.
func ExportRouteTokens(ctx context.Context, db *sql.DB) ([]ArchiveRouteTokenRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT token_hash, jid, owner_folder, created_at, COALESCE(context,'')
		 FROM route_tokens WHERE kind = 'route' ORDER BY jid`)
	if err != nil {
		return nil, fmt.Errorf("query route_tokens: %w", err)
	}
	defer rows.Close()
	var out []ArchiveRouteTokenRow
	for rows.Next() {
		var hashBytes []byte
		var r ArchiveRouteTokenRow
		if err := rows.Scan(&hashBytes, &r.JID, &r.OwnerFolder, &r.CreatedAt, &r.Context); err != nil {
			return nil, fmt.Errorf("scan route_token: %w", err)
		}
		r.TokenHash = hex.EncodeToString(hashBytes)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportRouteTokens UPSERTs by token_hash (the PK) — a route token's row
// content never legitimately changes once minted, so INSERT OR IGNORE is
// equivalent to UPSERT here. Every imported row's kind is forced to
// 'route': this function never writes a pairing token (rows carrying one
// were never exported in the first place, but the write side stays
// explicit rather than trusting the archive's own claim).
//
// Performs NO gating — the off-by-default / --force / proven-empty-target
// policy (spec 5/8 "Secret and token values" (2)) is `archive apply`
// orchestration in cmd/arizuko, not this primitive's job.
func ImportRouteTokens(ctx context.Context, db *sql.DB, rows []ArchiveRouteTokenRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, r := range rows {
		hashBytes, herr := hex.DecodeString(r.TokenHash)
		if herr != nil {
			return 0, fmt.Errorf("route_token %s: bad token_hash: %w", r.JID, herr)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO route_tokens (token_hash, jid, owner_folder, created_at, context, kind)
			 VALUES (?, ?, ?, ?, ?, 'route')`,
			hashBytes, r.JID, r.OwnerFolder, r.CreatedAt, nullStr(r.Context),
		); err != nil {
			return 0, fmt.Errorf("insert route_token %s: %w", r.JID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountRouteTokens counts EVERY row (both kind='route' and kind='pair') —
// the proven-empty-target gate (spec 5/8 "Secret and token values" (2))
// wants a genuinely fresh table, not merely zero route-kind rows.
func CountRouteTokens(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_tokens").Scan(&n)
	return n, err
}

// ArchiveInviteRow is one row of the archive's invites document: InvitesRow's
// shape (resreg/resources/invites.go) with Ref emitted — the config
// manifest hides it (yaml:"-") because Ref is the hash-at-rest PK, but the
// archive's UPSERT lane needs it as the conflict key.
type ArchiveInviteRow struct {
	Ref         string `yaml:"ref"`
	TargetGlob  string `yaml:"target_glob"`
	IssuedBySub string `yaml:"issued_by_sub"`
	IssuedAt    string `yaml:"issued_at"`
	ExpiresAt   string `yaml:"expires_at,omitempty"`
	MaxUses     int    `yaml:"max_uses"`
	UsedCount   int    `yaml:"used_count"`
}

// ExportInvites reads every invites row (onbod.db) with its ref hash-at-rest
// PK.
func ExportInvites(ctx context.Context, db *sql.DB) ([]ArchiveInviteRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT ref, target_glob, issued_by_sub, issued_at, COALESCE(expires_at,''), max_uses, used_count
		 FROM invites ORDER BY ref`)
	if err != nil {
		return nil, fmt.Errorf("query invites: %w", err)
	}
	defer rows.Close()
	var out []ArchiveInviteRow
	for rows.Next() {
		var r ArchiveInviteRow
		if err := rows.Scan(&r.Ref, &r.TargetGlob, &r.IssuedBySub, &r.IssuedAt, &r.ExpiresAt, &r.MaxUses, &r.UsedCount); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportInvites UPSERTs by ref (the PK) — matching ImportRouteTokens'
// reasoning: an invite's content never legitimately changes once issued
// (used_count moves via redemption, a separate imperative path, not via
// archive restore), so INSERT OR IGNORE is equivalent to UPSERT here.
// Performs NO gating — same split of responsibility as ImportRouteTokens.
func ImportInvites(ctx context.Context, db *sql.DB, rows []ArchiveInviteRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO invites (ref, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.Ref, r.TargetGlob, r.IssuedBySub, r.IssuedAt, nullStr(r.ExpiresAt), r.MaxUses, r.UsedCount,
		); err != nil {
			return 0, fmt.Errorf("insert invite %s: %w", r.Ref, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountInvites counts every row — the proven-empty-target gate's onbod-side
// twin of CountRouteTokens.
func CountInvites(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM invites").Scan(&n)
	return n, err
}

// --- small helpers --------------------------------------------------------

// nullStr maps an empty string to a bind-time NULL, matching the nullable
// TEXT columns these tables declare (route_tokens.context, messages.
// sender_name, etc.) — mirrors the same nilIfEmptyString idiom
// resreg/resources uses, kept local since this file's tables aren't
// resreg.Resource-registered.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// b2i converts a bool to the 0/1 SQLite stores INTEGER columns as.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
