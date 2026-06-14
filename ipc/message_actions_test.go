package ipc

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// Spec 5/Z — pin_message, unpin_message, unpin_all MCP tool integration.
// Tests verify the gateway wiring: tool calls dispatch to GatedFns.Pin/Unpin
// and gracefully handle missing capabilities (nil funcs → error, not panic).

func TestServeMCP_PinMessage(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var pinJID, pinTarget string
	gated := GatedFns{
		Pin: func(jid, targetID string) error {
			pinJID = jid
			pinTarget = targetID
			return nil
		},
	}
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string { return "world" },
	}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	resp := callRaw(t, sock, "pin_message", map[string]any{
		"chatJid":  "slack:C123",
		"targetId": "msg-456",
	})
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("pin_message returned error: %s", resp)
	}
	if pinJID != "slack:C123" {
		t.Errorf("Pin jid = %q, want slack:C123", pinJID)
	}
	if pinTarget != "msg-456" {
		t.Errorf("Pin targetID = %q, want msg-456", pinTarget)
	}
}

func TestServeMCP_UnpinMessage(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var unpinJID, unpinTarget string
	var unpinAll bool
	gated := GatedFns{
		Unpin: func(jid, targetID string, all bool) error {
			unpinJID = jid
			unpinTarget = targetID
			unpinAll = all
			return nil
		},
	}
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string { return "world" },
	}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	resp := callRaw(t, sock, "unpin_message", map[string]any{
		"chatJid":  "telegram:group/-100",
		"targetId": "msg-789",
	})
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("unpin_message returned error: %s", resp)
	}
	if unpinJID != "telegram:group/-100" {
		t.Errorf("Unpin jid = %q, want telegram:group/-100", unpinJID)
	}
	if unpinTarget != "msg-789" {
		t.Errorf("Unpin targetID = %q, want msg-789", unpinTarget)
	}
	if unpinAll {
		t.Error("unpin_message must pass all=false")
	}
}

func TestServeMCP_UnpinAll(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var unpinJID, unpinTarget string
	var unpinAll bool
	gated := GatedFns{
		Unpin: func(jid, targetID string, all bool) error {
			unpinJID = jid
			unpinTarget = targetID
			unpinAll = all
			return nil
		},
	}
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string { return "world" },
	}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	resp := callRaw(t, sock, "unpin_all", map[string]any{
		"chatJid": "slack:C999",
	})
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("unpin_all returned error: %s", resp)
	}
	if unpinJID != "slack:C999" {
		t.Errorf("Unpin jid = %q, want slack:C999", unpinJID)
	}
	if unpinTarget != "" {
		t.Errorf("unpin_all must pass empty targetID, got %q", unpinTarget)
	}
	if !unpinAll {
		t.Error("unpin_all must pass all=true")
	}
}

// Pin/Unpin tools with nil GatedFns return an error, not a panic.
func TestServeMCP_PinUnpin_NilFuncs(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	gated := GatedFns{} // Pin and Unpin are nil
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string { return "world" },
	}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"pin_message", map[string]any{"chatJid": "slack:C1", "targetId": "m1"}},
		{"unpin_message", map[string]any{"chatJid": "slack:C1", "targetId": "m1"}},
		{"unpin_all", map[string]any{"chatJid": "slack:C1"}},
	}
	for _, c := range cases {
		resp := callRaw(t, sock, c.tool, c.args)
		if !strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s with nil func should error, got: %s", c.tool, resp)
		}
		if !strings.Contains(resp, "not configured") {
			t.Errorf("%s error should mention 'not configured', got: %s", c.tool, resp)
		}
	}
}

// Unsupported error from the adapter surfaces as UnsupportedError hint.
func TestServeMCP_PinMessage_Unsupported(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	gated := GatedFns{
		Pin: func(jid, targetID string) error {
			return chanlib.ErrUnsupported
		},
	}
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string { return "world" },
	}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	resp := callRaw(t, sock, "pin_message", map[string]any{
		"chatJid":  "mastodon:home",
		"targetId": "m1",
	})
	if !strings.Contains(resp, `"isError":true`) {
		t.Fatalf("pin_message on unsupported platform should error: %s", resp)
	}
	// toolMaybeUnsupported wraps ErrUnsupported with a hint
	if !strings.Contains(resp, "unsupported") && !strings.Contains(resp, "Unsupported") {
		t.Errorf("error should mention unsupported: %s", resp)
	}
}

// Cross-folder authz: tier-2 folder cannot pin in a JID routed elsewhere.
func TestServeMCP_PinMessage_CrossFolderDenied(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	pinCalled := false
	gated := GatedFns{
		Pin: func(jid, targetID string) error {
			pinCalled = true
			return nil
		},
	}
	db := StoreFns{
		// JID is routed to "other", not "world/a/b"
		DefaultFolderForJID: func(jid string) string { return "other" },
	}
	// Tier-2 folder at world/a/b
	stop, err := ServeMCP(sock, gated, db, "world/a/b", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	resp := callRaw(t, sock, "pin_message", map[string]any{
		"chatJid":  "slack:foreign",
		"targetId": "m1",
	})
	if !strings.Contains(resp, `"isError":true`) {
		t.Fatalf("cross-folder pin should be denied: %s", resp)
	}
	if pinCalled {
		t.Error("Pin should not be called when authz fails")
	}
}
