package routd

import (
	"context"
	"reflect"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/store"
)

// Core is first whether or not a record names it. The others follow in the
// order they were selected, whatever their scope, and a tie goes to the
// instance before the group; a group's package is not a package of another
// group.
//
// The managed files are the group files the packages of that folder alone
// wrote, whatever the other folders and the instance hold.
func TestSelectedPackagesOrder(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	skills := map[string][]string{"skill": {"one"}}
	files := func(rels ...string) map[string][]string {
		return map[string][]string{"skill": {"one"}, container.ManifestGroupFiles: rels}
	}
	for _, record := range []InstalledPackage{
		{Folder: "main", Name: "later", InstalledAt: "2026-10-28T09:00:00.000000000Z", Manifest: files("PERSONA.md", "facts/b.md")},
		{Folder: InstanceWide, Name: "earlier", InstalledAt: "2026-10-28T08:00:00.000000000Z", Manifest: skills},
		{Folder: InstanceWide, Name: "core", InstalledAt: "2026-10-28T10:00:00.000000000Z", Manifest: skills},
		{Folder: "other", Name: "elsewhere", InstalledAt: "2026-10-28T07:00:00.000000000Z", Manifest: files("other.md")},
		{Folder: "main", Name: "first", InstalledAt: "2026-10-28T07:30:00.000000000Z", Manifest: files("facts/a.md")},
		{Folder: "main", Name: "aa-tie", InstalledAt: "2026-10-28T08:30:00.000000000Z", Manifest: skills},
		{Folder: InstanceWide, Name: "zz-tie", InstalledAt: "2026-10-28T08:30:00.000000000Z", Manifest: files("instance.md")},
	} {
		if err := d.PutInstalledPackage(record); err != nil {
			t.Fatal(err)
		}
	}
	got, managed, err := d.SelectedPackages("main")
	if want := []string{"core", "first", "earlier", "zz-tie", "aa-tie", "later"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v, %v; want %v", got, err, want)
	}
	if want := []string{"PERSONA.md", "facts/a.md", "facts/b.md"}; !reflect.DeepEqual(managed, want) {
		t.Fatalf("managed %v; want %v", managed, want)
	}
}

// Core is implicit: a turn mounts it with no record in the store.
func TestSelectedPackagesCoreNeedsNoRecord(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, managed, err := d.SelectedPackages("main")
	if want := []string{"core"}; err != nil || !reflect.DeepEqual(got, want) || len(managed) != 0 {
		t.Fatalf("selected %v, managed %v, %v; want %v", got, managed, err, want)
	}
}

// The first boot selects the product `arizuko create -p` chose. A later boot
// leaves the selection to the operator: a withdrawn product stays withdrawn
// and a kept one keeps its place in the order.
func TestOpenSelectsDefaultProductOnce(t *testing.T) {
	t.Setenv("ARIZUKO_DEFAULT_PRODUCT", "pm")
	t.Cleanup(func() {
		h, err := store.Handle(store.OwnerRoutd)
		if err != nil {
			t.Error(err)
			return
		}
		left, _ := h.Entities(context.Background())
		for _, entity := range left {
			_ = h.DropEntity(context.Background(), entity)
		}
	})
	boot := func() InstalledPackage {
		t.Helper()
		d, err := Open()
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		record, _, err := d.InstalledPackage(store.DefaultFolder, "pm")
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	first := boot()
	if first.Source != "ant/examples/pm" || first.InstalledAt == "" {
		t.Fatalf("first boot selection: %+v", first)
	}
	if second := boot(); !reflect.DeepEqual(second, first) {
		t.Fatalf("second boot rewrote the selection:\n%+v\n%+v", first, second)
	}
	d, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DeleteInstalledPackage(store.DefaultFolder, "pm"); err != nil || !ok {
		t.Fatalf("withdraw: %v, %v", ok, err)
	}
	d.Close()
	if third := boot(); third.Name != "" {
		t.Fatalf("a later boot selected the withdrawn product again: %+v", third)
	}
}
