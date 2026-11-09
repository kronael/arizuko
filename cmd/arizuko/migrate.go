package main

import (
	"github.com/kronael/arizuko/compose"
)

func cmdMigrate(args []string) {
	args = need(args, 1, 1, "arizuko migrate -i <instance>")
	if err := compose.MigrateEnv(mustInstanceDir(args[0])); err != nil {
		die("Failed: %v", err)
	}
}
