// Package services holds the service packages this release ships, one
// directory each: the compose fragment and its arizuko.yml. The arizuko binary
// carries them, so an instance installs a release service from the binary it
// runs and never from a checkout on the host.
package services

import "embed"

//go:embed */*.yml
var FS embed.FS
