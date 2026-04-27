package container

import (
	"testing"
)

// fakeResolver is a SecretsResolver test double.
type fakeResolver struct {
	folderRet  map[string]string
	folderErr  error
	userRet    map[string]string
	userErr    error
	isGroupOf  map[string]bool
	subOfJID   map[string]string
	folderArg  string
	userArg    string
	isGroupArg string
}

func (f *fakeResolver) FolderSecretsResolved(folder string) (map[string]string, error) {
	f.folderArg = folder
	return f.folderRet, f.folderErr
}

func (f *fakeResolver) UserSecrets(userSub string) (map[string]string, error) {
	f.userArg = userSub
	return f.userRet, f.userErr
}

func (f *fakeResolver) GetChatIsGroup(jid string) bool {
	f.isGroupArg = jid
	return f.isGroupOf[jid]
}

func (f *fakeResolver) UserSubByJID(jid string) (string, bool) {
	sub, ok := f.subOfJID[jid]
	return sub, ok
}

func TestResolveSpawnEnv_InjectsFolderSecrets(t *testing.T) {
	r := &fakeResolver{
		folderRet: map[string]string{"KEY": "v1"},
		isGroupOf: map[string]bool{"jid:1": true}, // group → no user overlay
	}
	base := map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"}

	got := resolveSpawnEnv(r, base, "atlas", "jid:1")

	if got["KEY"] != "v1" {
		t.Errorf("KEY = %q, want v1", got["KEY"])
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
		t.Errorf("base token missing: %q", got["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if r.folderArg != "atlas" {
		t.Errorf("FolderSecretsResolved called with %q, want atlas", r.folderArg)
	}
}

func TestResolveSpawnEnv_UserSecretsOverlayInSingleUserChat(t *testing.T) {
	r := &fakeResolver{
		folderRet: map[string]string{"KEY": "folder-val", "FOLDER_ONLY": "f"},
		userRet:   map[string]string{"KEY": "user-val", "USER_ONLY": "u"},
		isGroupOf: map[string]bool{"jid:dm": false}, // single-user
		subOfJID:  map[string]string{"jid:dm": "github:alice"},
	}
	base := map[string]string{}

	got := resolveSpawnEnv(r, base, "atlas/eng", "jid:dm")

	if got["KEY"] != "user-val" {
		t.Errorf("KEY = %q, want user-val (user overlays folder)", got["KEY"])
	}
	if got["FOLDER_ONLY"] != "f" {
		t.Errorf("FOLDER_ONLY = %q, want f", got["FOLDER_ONLY"])
	}
	if got["USER_ONLY"] != "u" {
		t.Errorf("USER_ONLY = %q, want u", got["USER_ONLY"])
	}
	if r.userArg != "github:alice" {
		t.Errorf("UserSecrets called with %q, want github:alice", r.userArg)
	}
}

func TestResolveSpawnEnv_NoUserSecretsInGroupChat(t *testing.T) {
	r := &fakeResolver{
		folderRet: map[string]string{"FOLDER_KEY": "f"},
		userRet:   map[string]string{"USER_KEY": "u"},
		isGroupOf: map[string]bool{"jid:grp": true},
		subOfJID:  map[string]string{"jid:grp": "github:alice"},
	}
	base := map[string]string{}

	got := resolveSpawnEnv(r, base, "atlas", "jid:grp")

	if _, ok := got["USER_KEY"]; ok {
		t.Errorf("USER_KEY leaked into group chat env: %v", got)
	}
	if got["FOLDER_KEY"] != "f" {
		t.Errorf("FOLDER_KEY = %q, want f", got["FOLDER_KEY"])
	}
	if r.userArg != "" {
		t.Errorf("UserSecrets should not be called for group chat, was called with %q", r.userArg)
	}
}

func TestResolveSpawnEnv_NilResolverReturnsBase(t *testing.T) {
	base := map[string]string{"X": "y"}
	got := resolveSpawnEnv(nil, base, "atlas", "jid:1")
	if got["X"] != "y" || len(got) != 1 {
		t.Errorf("nil resolver must pass base unchanged, got %v", got)
	}
}

func TestResolveSpawnEnv_FolderErrFallsBackToBase(t *testing.T) {
	r := &fakeResolver{folderErr: errFolderTest}
	base := map[string]string{"X": "y"}
	got := resolveSpawnEnv(r, base, "atlas", "jid:1")
	if got["X"] != "y" || len(got) != 1 {
		t.Errorf("folder err must fall back to base, got %v", got)
	}
}

func TestResolveSpawnEnv_NoUserJIDMappingInSingleUserChat(t *testing.T) {
	r := &fakeResolver{
		folderRet: map[string]string{"K": "v"},
		isGroupOf: map[string]bool{"jid:dm": false},
		// subOfJID empty — no row in user_jids
	}
	got := resolveSpawnEnv(r, nil, "atlas", "jid:dm")
	if got["K"] != "v" {
		t.Errorf("folder secret missing: %v", got)
	}
	if r.userArg != "" {
		t.Errorf("UserSecrets called despite no JID mapping: arg=%q", r.userArg)
	}
}

// errFolderTest is a sentinel for fall-back tests.
var errFolderTest = &testErr{"boom"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }
