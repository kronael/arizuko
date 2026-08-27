package main

import (
	"os"
	"path/filepath"
	"testing"
)

// loadConfig and openDB are emaid's whole bootstrap, and both were 0%. The
// defaults matter: an operator who sets only the four credentials gets these,
// and a wrong port or an empty data dir is a daemon that starts and then never
// receives a message.
func TestLoadConfigDefaults(t *testing.T) {
	for _, k := range []string{
		"CHANNEL_NAME", "EMAIL_IMAP_PORT", "EMAIL_SMTP_PORT", "LISTEN_ADDR",
		"LISTEN_URL", "DATA_DIR", "MEDIA_MAX_FILE_BYTES",
	} {
		t.Setenv(k, "")
	}
	for _, k := range []string{
		"EMAIL_IMAP_HOST", "EMAIL_SMTP_HOST", "EMAIL_ACCOUNT", "EMAIL_PASSWORD", "ROUTER_URL",
	} {
		t.Setenv(k, "set-"+k)
	}

	c := loadConfig()
	if c.Name != "email" {
		t.Errorf("Name = %q, want the email default", c.Name)
	}
	// 993 is IMAPS and 587 is submission. Defaulting either to the plaintext
	// port would send the account password in the clear.
	if c.IMAPPort != "993" {
		t.Errorf("IMAPPort = %q, want 993 (IMAPS)", c.IMAPPort)
	}
	if c.SMTPPort != "587" {
		t.Errorf("SMTPPort = %q, want 587 (submission)", c.SMTPPort)
	}
	if c.MaxAttachment != 20*1024*1024 {
		t.Errorf("MaxAttachment = %d, want 20MiB", c.MaxAttachment)
	}
	if c.DataDir == "" || c.ListenAddr == "" || c.ListenURL == "" {
		t.Errorf("an empty default would make the daemon unreachable: %+v", c)
	}
	if c.IMAPHost != "set-EMAIL_IMAP_HOST" || c.Password != "set-EMAIL_PASSWORD" {
		t.Errorf("required vars not carried through: %+v", c)
	}

	t.Setenv("EMAIL_IMAP_PORT", "143")
	t.Setenv("MEDIA_MAX_FILE_BYTES", "4096")
	c = loadConfig()
	if c.IMAPPort != "143" || c.MaxAttachment != 4096 {
		t.Errorf("overrides not read: %+v", c)
	}

	// A non-numeric byte limit falls back rather than becoming 0, which would
	// reject every attachment.
	t.Setenv("MEDIA_MAX_FILE_BYTES", "twenty megs")
	if got := loadConfig().MaxAttachment; got != 20*1024*1024 {
		t.Errorf("an unparseable limit gave %d, want the default", got)
	}
}

// emaid.db is adapter-private state that `arizuko create` does not seed, so
// openDB must create the directory and the file itself, migrate BOTH chains
// into it, and be safe to call again on the next boot.
func TestOpenDBCreatesMigratesAndReopens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "emaid")

	db, err := openDB(dir)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "emaid.db")); err != nil {
		t.Fatalf("openDB did not create the file: %v", err)
	}

	// chanlib's outbound_dedup lands in the SAME file as emaid's own chain,
	// keyed on (service, version). store_test.go covers emaid's tables; this
	// is the second chain, without which a send dedups against a table that
	// is not there.
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='outbound_dedup'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("chanlib's outbound_dedup chain did not run in emaid.db")
	}

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	db.Close()

	// The second boot must not re-run a migration or fail on an existing table.
	db2, err := openDB(dir)
	if err != nil {
		t.Fatalf("second openDB on an existing file: %v", err)
	}
	db2.Close()
}
