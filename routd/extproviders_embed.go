package routd

import "embed"

//go:embed extproviders/*.toml
var builtinProviders embed.FS
