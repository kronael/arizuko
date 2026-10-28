package main

// A rows file is a partial dump: `arizuko apply` writes the rows it names and
// deletes nothing it does not name (spec 5/8 §"Rows file").

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

func TestApplyRows_WritesNamedRowsOnly(t *testing.T) {
	ctx := context.Background()
	dataDir, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	seedGroupFiles(t, dataDir, "atlas")
	seedGroupFiles(t, dataDir, "ops")
	if _, err := resreg.Apply(ctx, resreg.On(st.Obj()), resreg.SubsystemRoutd, routdChecksum(t, st), false, map[string]any{
		"groups": []resources.GroupsRow{
			{Folder: "atlas", Product: "assistant", ContainerConfig: `{"timeout":30}`},
			{Folder: "ops", Product: "assistant", ContainerConfig: `{"timeout":99}`},
		},
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "platform=tele", Target: "atlas"},
			{Seq: 1, Match: "platform=slack", Target: "ops"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	file := `
groups:
  - folder: atlas
    product: assistant
    container_config: '{"timeout":60}'
routes:
  - seq: 0
    match: platform=tele
    target: ops
`
	docs, err := parseDocs("rows.yml", []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := mergeLive(ctx, stores, docs)
	if err != nil {
		t.Fatal(err)
	}
	deltas, err := resreg.Plan(resreg.On(st.Obj()), resreg.SubsystemRoutd, merged[0].manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deltas {
		if len(d.Remove) > 0 {
			t.Errorf("plan removes %v from %s; a rows file removes nothing", d.Remove, d.Resource)
		}
	}

	lines, err := applyRows(ctx, stores, dataDir, "rows.yml", []byte(file))
	if err != nil {
		t.Fatalf("applyRows: %v", err)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "routd: applied") {
		t.Errorf("report = %q, want one routd line", lines)
	}

	got, err := resreg.Lookup("groups").ScanAll(resreg.On(st.Obj()))
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]string{}
	for _, g := range got.([]resources.GroupsRow) {
		config[g.Folder] = g.ContainerConfig
	}
	if len(config) != 2 || config["atlas"] != `{"timeout":60}` || config["ops"] != `{"timeout":99}` {
		t.Errorf("container_config by folder = %v, want atlas 60 and ops 99 untouched", config)
	}
	routes, err := resreg.Lookup("routes").ScanAll(resreg.On(st.Obj()))
	if err != nil {
		t.Fatal(err)
	}
	targets := map[int]string{}
	for _, r := range routes.([]resources.RoutesRow) {
		targets[r.Seq] = r.Target
	}
	if len(targets) != 2 || targets[0] != "ops" || targets[1] != "ops" {
		t.Errorf("route targets by seq = %v, want seq 0 rewritten and seq 1 kept", targets)
	}
}

func TestIsTar_DumpIsTarRowsFileIsNot(t *testing.T) {
	dataDir, stores := openInstance(t)
	var buf bytes.Buffer
	if _, err := buildArchive(context.Background(), stores, dataDir, false, &buf); err != nil {
		t.Fatal(err)
	}
	if !isTar(buf.Bytes()) {
		t.Error("a dump is not recognized as a tar")
	}
	if isTar([]byte("groups:\n  - folder: atlas\n")) {
		t.Error("a rows file is taken for a tar")
	}
}
