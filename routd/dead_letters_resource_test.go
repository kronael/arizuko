package routd

// Tests for routd's dead-letter surface (BUGS L23): one handler, two faces, two
// injected gates, plus the replay that puts a lost message back on the existing
// outbound sweep.
//
// The containment tests are adversarial in the way the recorded near-miss was
// not: the agent ASKS for the rival folder by name, and replays a rival id by
// name. A test that only asked for its own folder would be answered by the
// ordinary filter and would still pass with the pin deleted.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// deadLetterFixture seeds two tenants plus a child folder, and one
// dead-lettered bot row per folder alongside a row that is NOT dead-lettered.
// It answers the rows it wrote as well, because a delivery operation is
// addressed by the MESSAGE rather than by an id.
//
// The count assertion below matters: every containment check is "row X is
// absent", which an empty result satisfies.
func deadLetterFixture(t *testing.T) (*DB, map[string]core.Message) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"hq", "hq/support", "rival"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct{ id, folder, status string }{
		{"dl-hq", "hq", core.MessageStatusFailed},
		{"dl-hq-child", "hq/support", core.MessageStatusFailed},
		{"dl-rival-secret", "rival", core.MessageStatusFailed},
		{"still-pending-hq", "hq", core.MessageStatusPending},
	}
	now := time.Now()
	msgs := map[string]core.Message{}
	for i, r := range rows {
		m := core.Message{
			ID: r.id, ChatJID: "slack:T/C" + string(rune('0'+i)) + "/U",
			Content: "lost text " + r.id, Timestamp: now.Add(time.Duration(i) * time.Second),
			BotMsg: true, RoutedTo: r.folder,
		}
		if err := db.PutOutbound(m, r.status); err != nil {
			t.Fatal(err)
		}
		msgs[r.id] = m
	}
	dead, err := db.DeadLetters("", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 3 {
		t.Fatalf("fixture wrote %d dead letters, want 3 — every assertion below "+
			"would pass vacuously", len(dead))
	}
	return db, msgs
}

func serveDeadLetterMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.deadLettersPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder}),
		srv.turnVisible(turnMCP{folder: folder}))
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// The whole point of L23: the operator could see a COUNT and not the content.
// The row must carry what was lost, to whom, and how many attempts it took.
func TestListDeadLettersReturnsTheLostContent(t *testing.T) {
	db, _ := deadLetterFixture(t)
	rows, err := db.DeadLetters("", 0)
	if err != nil {
		t.Fatalf("DeadLetters: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Content == "" || r.ChatJID == "" {
			t.Errorf("dead letter %s has no content/chat — a count with extra steps: %+v", r.ID, r)
		}
	}
}

// A pending row is not a dead letter: the sweep is still going to send it, and
// listing it would tell an operator a live message was lost.
func TestListDeadLettersExcludesPending(t *testing.T) {
	db, _ := deadLetterFixture(t)
	rows, _ := db.DeadLetters("", 0)
	for _, r := range rows {
		if r.ID == "still-pending-hq" {
			t.Fatalf("a pending row was listed as dead-lettered: %+v", r)
		}
	}
}

func TestListDeadLettersAgentSeesOnlyItsOwnSubtree(t *testing.T) {
	db, _ := deadLetterFixture(t)
	grantAt(t, db, "folder:hq", "mcp:list_dead_letters", "hq/**")
	grantAt(t, db, "folder:hq", "mcp:list_dead_letters", "hq")
	sock := serveDeadLetterMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "list_dead_letters", map[string]any{"folder": "rival"})
	if e != "" {
		t.Fatalf("list_dead_letters errored: %s", e)
	}
	if strings.Contains(text, "dl-rival-secret") {
		t.Errorf("folder=rival widened past the socket folder — cross-tenant leak: %s", text)
	}
	if !strings.Contains(text, "dl-hq-child") {
		t.Errorf("subtree row missing — containment is by subtree, not exact match: %s", text)
	}
	if !strings.Contains(text, "dl-hq") {
		t.Errorf("own row missing: %s", text)
	}
}

func TestListDeadLettersDeniedWithoutGrant(t *testing.T) {
	db, _ := deadLetterFixture(t)
	grantAt(t, db, "folder:hq", "mcp:list_dead_letters", "rival/**")
	sock := serveDeadLetterMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "list_dead_letters", nil)
	if e == "" {
		t.Fatalf("list_dead_letters should be denied at a foreign scope, got: %s", text)
	}
	if strings.Contains(e, "lost text") || strings.Contains(text, "lost text") {
		t.Errorf("denial leaked a row: %s / %s", e, text)
	}
}

// Replay is a mutation on another tenant's message if containment is missing.
// The agent names the rival id directly.
func TestReplayDeadLetterRefusesAForeignMessage(t *testing.T) {
	db, _ := deadLetterFixture(t)
	grantAt(t, db, "folder:hq", "mcp:replay_dead_letter", "hq/**")
	grantAt(t, db, "folder:hq", "mcp:replay_dead_letter", "hq")
	sock := serveDeadLetterMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "replay_dead_letter", map[string]any{"id": "dl-rival-secret"})
	if e == "" {
		t.Fatalf("replaying another tenant's message succeeded: %s", text)
	}
	// The dead-letter set is the only place a failed row lives, so still being
	// readable from it IS the proof that nothing moved.
	if _, ok := db.DeadLetterByID("dl-rival-secret"); !ok {
		t.Fatal("the rival row left the dead-letter set")
	}
}

// Replay hands the row back to the EXISTING outbound sweep — status 'pending'
// with a fresh budget is exactly what PendingOutbound selects. No second
// delivery path.
func TestReplayDeadLetterRequeuesForTheExistingSweep(t *testing.T) {
	db, msgs := deadLetterFixture(t)
	// Spend the row's whole budget so it dead-letters with attempts on it: a
	// replay that kept them would die again on its first retry.
	owed := msgs["still-pending-hq"]
	n, died, err := db.RecordDeliveryFailure(owed, 1)
	if err != nil || !died || n != 1 {
		t.Fatalf("seed attempts: n=%d died=%v err=%v", n, died, err)
	}
	moved, err := db.ReplayDeadLetter(owed)
	if err != nil || !moved {
		t.Fatalf("ReplayDeadLetter = %v, %v; want true, nil", moved, err)
	}
	pending, err := db.PendingOutbound(time.Now().Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("PendingOutbound: %v", err)
	}
	var found bool
	for _, m := range pending {
		if m.ID == owed.ID {
			found = true
			if m.Attempts != 0 {
				t.Errorf("replayed row kept %d attempts — it would dead-letter again at once",
					m.Attempts)
			}
		}
	}
	if !found {
		t.Fatal("replayed row is not in the outbound sweep's feed")
	}
}

// Replaying a row that is no longer dead-lettered must not read as success:
// this call queued nothing.
func TestReplayDeadLetterIsNotIdempotentlyTrue(t *testing.T) {
	db, msgs := deadLetterFixture(t)
	if moved, err := db.ReplayDeadLetter(msgs["dl-hq"]); err != nil || !moved {
		t.Fatalf("first replay = %v, %v", moved, err)
	}
	if moved, err := db.ReplayDeadLetter(msgs["dl-hq"]); err != nil || moved {
		t.Fatalf("second replay = %v, %v; want false, nil", moved, err)
	}
	if moved, err := db.ReplayDeadLetter(msgs["still-pending-hq"]); err != nil || moved {
		t.Fatalf("replaying a pending row = %v, %v; want false, nil", moved, err)
	}
}

// The REST face: 403 without the scope, with the positive control beside it so
// a broken mount fails both.
func TestDeadLettersRESTRequiresScope(t *testing.T) {
	db, _ := deadLetterFixture(t)
	get := func(scope []string) (int, string) {
		srv := NewServer(db, nil, nil, scopeVerifier{sub: "service:x", scope: scope}, 0, "")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/dead_letters", nil))
		return rec.Code, rec.Body.String()
	}

	code, body := get([]string{"routes:read"})
	if code != http.StatusForbidden {
		t.Fatalf("status = %d without dead_letters:read, want 403: %s", code, body)
	}
	if strings.Contains(body, "lost text") {
		t.Errorf("denied response leaked a row: %s", body)
	}

	code, body = get([]string{"dead_letters:read"})
	if code != http.StatusOK {
		t.Fatalf("status = %d with dead_letters:read, want 200: %s", code, body)
	}
	var rows []struct {
		ID               string `json:"id"`
		Content          string `json:"content"`
		DeliveryAttempts int    `json:"delivery_attempts"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("not a row array (%v): %s", err, body)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want all 3 — a claimless service token reads "+
			"instance-wide: %s", len(rows), body)
	}
	// The body must use the field names /openapi.json reflects off
	// DeadLetterRow. core.Message has no json tags, so returning it raw would
	// ship Go names and the two declarations would disagree on every field.
	for _, r := range rows {
		if r.ID == "" || r.Content == "" {
			t.Fatalf("row decoded empty — the response shape does not match the "+
				"declared RowType: %s", body)
		}
	}
}

// A replay over REST needs messages:WRITE, not just read: it puts a message
// back on the wire.
func TestDeadLetterReplayRESTRequiresWriteScope(t *testing.T) {
	db, _ := deadLetterFixture(t)
	post := func(scope []string) int {
		srv := NewServer(db, nil, nil, scopeVerifier{sub: "service:x", scope: scope}, 0, "")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec,
			httptest.NewRequest("POST", "/v1/dead_letters/dl-hq/replay", nil))
		return rec.Code
	}
	if code := post([]string{"dead_letters:read"}); code != http.StatusForbidden {
		t.Fatalf("replay with only dead_letters:read = %d, want 403", code)
	}
	if _, ok := db.DeadLetterByID("dl-hq"); !ok {
		t.Fatal("a denied replay moved the row out of the dead-letter set")
	}
	if code := post([]string{"dead_letters:write"}); code != http.StatusOK {
		t.Fatalf("replay with dead_letters:write = %d, want 200", code)
	}
}

// DeadLetters scopes on `routed_to` for a grant, so a widened predicate is a
// cross-folder read, not a wrong list. A folder name may hold `_` and `%` and
// SQL LIKE folds ASCII case, so the predicate is substr (BUGS L252).
func TestDeadLettersFolderScopeIsNotWidenedByLikeMetacharacters(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The decoys need CHILDREN: `LIKE 'a_b/%'` widens on the subtree half of
	// the predicate, so a decoy with no child is matched by neither predicate
	// and proves nothing.
	folders := []string{"a_b", "a_b/one", "aXb", "aXb/two", "A_B", "A_B/three"}
	for i, f := range folders {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutOutbound(core.Message{
			ID: "dl-" + f, ChatJID: "slack:T/C" + string(rune('0'+i)) + "/U",
			Content: "lost text", Timestamp: time.Now(),
			BotMsg: true, RoutedTo: f,
		}, core.MessageStatusFailed); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.DeadLetters("a_b", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("DeadLetters(a_b) returned %d rows, want 2 — a_b and a_b/one only", len(rows))
	}
	for _, r := range rows {
		if r.RoutedTo != "a_b" && r.RoutedTo != "a_b/one" {
			t.Errorf("DeadLetters(a_b) leaked %q, which a grant on a_b does not cover", r.RoutedTo)
		}
	}
}
