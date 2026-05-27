// Package resources declares the canonical Row structs + Register
// calls for every cold-tier resource manifested in spec 5/36.
//
// One file per resource — adding a resource is a small Go file (Row
// struct + init() block). The engine drives SELECT/INSERT/DELETE +
// YAML parse/emit; per-resource code only carries hooks for things
// the engine cannot deduce from struct tags (nullable columns mapped
// to non-pointer Go fields, JSON-blob columns, encryption, default
// timestamps).
//
// Import for side effects:
//
//	import _ "github.com/kronael/arizuko/resreg/resources"
//
// before any code that calls resreg.Apply / resreg.Export.
package resources
