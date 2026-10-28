package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
)

// `packages add` provisions what the package's manifest needs from .env and
// applies its rows through the same path `install` uses: a `generate` key is
// minted into .env, so the instance comes up on the next generate with no
// hand-written secret (the litellm package used to install cleanly and then
// refuse to start until the operator wrote MODEL_GATEWAY_KEY by hand), and a
// declared row lands with a record that names it, so `remove` can withdraw it.

// seedRoutdDB gives an instance the routd.db every real one has.
func seedRoutdDB(t *testing.T, dataDir string) {
	t.Helper()
	t.Cleanup(func() { dropInstance(t) })
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	rdb.Close()
}

// A `required` need with no operator value refuses at install time, naming the
// key, and leaves no fragment behind — never a services/ entry that generate
// would include and docker would then fail to start.
