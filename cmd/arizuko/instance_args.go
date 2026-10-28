package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kronael/arizuko/core"
)

// instanceArgs takes -i/--instance out of args wherever it stands before "--"
// and returns the instance name, then the other arguments. Without the flag the
// instance comes from DATA_DIR, ARIZUKO_INSTANCE or FLAVOR.
func instanceArgs(args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	instance, selected := "", false
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		flag, value, inline := strings.Cut(args[i], "=")
		if flag != "-i" && flag != "--instance" {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 == len(args) {
				return nil, fmt.Errorf("%s needs an instance", flag)
			}
			i++
			value = args[i]
		}
		instance, selected = value, true
	}
	if selected {
		dir, err := instanceDir(instance)
		if err != nil {
			return nil, err
		}
		return append([]string{instance}, rest...), os.Setenv("DATA_DIR", dir)
	}
	_ = godotenv.Load(".env")
	if os.Getenv("DATA_DIR") == "" {
		instance = core.EnvOr("ARIZUKO_INSTANCE", os.Getenv("FLAVOR"))
	}
	return append([]string{instance}, rest...), nil
}
