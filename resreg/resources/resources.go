// Package resources declares the canonical Row structs + Register calls for
// every cold-tier resource (spec 5/8), one file per resource. Import for side
// effects before any code that calls resreg.Apply / resreg.Export:
//
//	import _ "github.com/kronael/arizuko/resreg/resources"
package resources
