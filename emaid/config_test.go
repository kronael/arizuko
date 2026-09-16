package main

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// loadConfig and openStorage are emaid's whole bootstrap. The
// defaults matter: an operator who sets only the four credentials gets these,
// and a wrong port or an empty data dir is a daemon that starts and then never
// receives a message.
func TestLoadConfigDefaults(t *testing.T) {
	for _, k := range []string{
		"CHANNEL_NAME", "EMAIL_IMAP_PORT", "EMAIL_SMTP_PORT", "LISTEN_ADDR",
		"LISTEN_URL", "MEDIA_MAX_FILE_BYTES",
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
	if c.ListenAddr == "" || c.ListenURL == "" {
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

// emaid's tables are adapter-private state nothing else prepares, so
// openStorage must make BOTH the entities emaid reads and the ones chanlib
// writes, and be safe to call again on the next boot.
func TestOpenStorageCreatesTheEntitiesAndReopens(t *testing.T) {
	t.Cleanup(dropEmaidEntities)

	as, st, err := openStorage()
	if err != nil {
		t.Fatalf("openStorage: %v", err)
	}

	// The store answers, which is the only thing emaid asks of it. emaistore's
	// own bodies cover what it answers; this is that the driver was given its
	// entities at all, without which every link fails on a missing table.
	if err := st.LinkMessageToThread("boot@x.com", "boot", "alice@x.com", "boot@x.com"); err != nil {
		t.Fatalf("the entities were not created: %v", err)
	}

	// chanlib's entities land under the SAME prefix, and the adapter mux reads
	// them; without them a send dedups against nothing. Asked through the store,
	// because emaid's own driver is a choice this file must not encode.
	claim, err := as.Chan.ClaimDelivery(t.Context(), "boot-key", "email:boot")
	if err != nil {
		t.Fatalf("chanlib's entities were not created in emaid.db: %v", err)
	}
	if claim.State != chanstore.ClaimWon {
		t.Errorf("the first claim on a fresh store is %v, want ClaimWon", claim.State)
	}
	as.Close() //nolint:errcheck // the test is ending

	// The second boot must not fail on the entities already there.
	as2, st2, err := openStorage()
	if err != nil {
		t.Fatalf("second openStorage on an existing file: %v", err)
	}
	if got, ok := st2.Thread("boot"); !ok || got.FromAddress != "alice@x.com" {
		t.Errorf("the reopened store answers %+v (found=%v); the rows did not survive", got, ok)
	}
	// The claim survives too, which is the half that makes a redelivery after a
	// restart return the recorded id rather than posting again.
	if claim, err := as2.Chan.ClaimDelivery(t.Context(), "boot-key", "email:boot"); err != nil ||
		claim.State == chanstore.ClaimWon {
		t.Errorf("the reopened claim is %v (err=%v); it must not be winnable again", claim.State, err)
	}
	as2.Close()
}

// dropEmaidEntities removes what openStorage made, so the endpoint does not
// outlive the run holding this adapter's tables.
func dropEmaidEntities() {
	h, err := dynstore.FromEnv(dynstore.Prefix("emaid"))
	if err != nil {
		return
	}
	ctx := context.Background()
	left, err := h.Entities(ctx)
	if err != nil {
		return
	}
	for _, e := range left {
		_ = h.DropEntity(ctx, e)
	}
}
