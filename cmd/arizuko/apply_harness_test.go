package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/resreg"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// spec 6/53 §Contract: a group's harness document survives a bulk import of a
// rows file and reaches the run request as the same value, compacted.
func TestApplyRows_CarriesHarnessDocument(t *testing.T) {
	ctx := context.Background()
	dataDir, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	seedGroupFiles(t, dataDir, "atlas")
	const harness = `{ "z" : {"unknown": [9007199254740993, 1.50, "\u0061\"q\""]}, "backend" : "codex" }`
	file := "groups:\n  - folder: atlas\n    product: assistant\n    config: '{\"harness\": " + harness + "}'\n"
	if _, err := applyRows(ctx, stores, dataDir, "rows.yml", []byte(file)); err != nil {
		t.Fatalf("applyRows: %v", err)
	}
	got, _, _, err := st.GroupConfig("atlas")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(runedv1.RunRequest{Harness: got})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Harness json.RawMessage `json:"harness"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(harness)); err != nil {
		t.Fatal(err)
	}
	if string(sent.Harness) != want.String() {
		t.Fatalf("run request harness = %s, want %s", sent.Harness, want.String())
	}
}
