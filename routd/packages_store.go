package routd

import "github.com/kronael/arizuko/store"

type InstalledPackage = store.InstalledPackage

const InstanceWide = ""

func (d *DB) PutInstalledPackage(p InstalledPackage) error {
	return d.store().PutInstalledPackage(p)
}

func (d *DB) InstalledPackage(folder, name string) (InstalledPackage, bool, error) {
	return d.store().InstalledPackage(folder, name)
}

func (d *DB) InstalledPackages() ([]InstalledPackage, error) {
	return d.store().InstalledPackages()
}

func (d *DB) DeleteInstalledPackage(folder, name string) (bool, error) {
	return d.store().DeleteInstalledPackage(folder, name)
}
