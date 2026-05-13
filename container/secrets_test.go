package container

import (
	"testing"
)

// fakeResolver is a SecretsResolver test double.
type fakeResolver struct {
	folderRet map[string]string
	folderErr error
	folderArg string
}

func (f *fakeResolver) FolderSecretsResolved(folder string) (map[string]string, error) {
	f.folderArg = folder
	return f.folderRet, f.folderErr
}

func TestResolveSpawnEnv_InjectsFolderSecrets(t *testing.T) {
	r := &fakeResolver{
		folderRet: map[string]string{"KEY": "v1"},
	}
	base := map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"}

	got := resolveSpawnEnv(r, base, "atlas")

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

func TestResolveSpawnEnv_NilResolverReturnsBase(t *testing.T) {
	base := map[string]string{"X": "y"}
	got := resolveSpawnEnv(nil, base, "atlas")
	if got["X"] != "y" || len(got) != 1 {
		t.Errorf("nil resolver must pass base unchanged, got %v", got)
	}
}

func TestResolveSpawnEnv_FolderErrFallsBackToBase(t *testing.T) {
	r := &fakeResolver{folderErr: errFolderTest}
	base := map[string]string{"X": "y"}
	got := resolveSpawnEnv(r, base, "atlas")
	if got["X"] != "y" || len(got) != 1 {
		t.Errorf("folder err must fall back to base, got %v", got)
	}
}

// errFolderTest is a sentinel for fall-back tests.
var errFolderTest = &testErr{"boom"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }
